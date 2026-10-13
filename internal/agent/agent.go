// Package agent runs the LLM tool loop that turns collected material into platform drafts.
//
// Tool policy (FR-4.2): the agent sees exactly two tools - web_search (read-only research) and
// submit_drafts (returns drafts to CueFlow). Publishing is not reachable from here: no publisher is
// passed in, the loop rejects any tool name outside its allowlist, and the context is marked as an
// agent context so a publish attempt fails in package guard even if one were wired in by mistake.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/guard"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/textutil"
)

const (
	toolSearch = "web_search"
	toolSubmit = "submit_drafts"
)

type Input struct {
	Topic         string
	Instructions  string
	Language      string
	Platforms     []string
	Materials     []Material
	SearchQueries []string
	CanSearch     bool
}

type Draft struct {
	Platform    string   `json:"platform"`
	Title       string   `json:"title"`
	Content     string   `json:"content"`
	ThreadParts []string `json:"thread_parts"`
	Hashtags    []string `json:"hashtags"`
	ImagePrompt string   `json:"image_prompt"`
	SourceRefs  []string `json:"source_refs"`
}

type Result struct {
	Drafts        []Draft
	SearchResults []connector.SearchResult
	ToolCalls     int
	Usage         llm.Usage
}

type Generator struct {
	LLM            llm.Chatter
	Search         connector.Searcher // optional
	MaxTurns       int
	MaxSearches    int
	MaxContentChar int
	Logf           func(level, msg string)
}

var ErrNoDrafts = errors.New("agent finished without submitting drafts")

func (g *Generator) logf(level, format string, args ...any) {
	if g.Logf != nil {
		g.Logf(level, fmt.Sprintf(format, args...))
	}
}

func (g *Generator) tools(canSearch bool, platforms []string) []llm.Tool {
	var tools []llm.Tool
	if canSearch {
		tools = append(tools, llm.Tool{
			Name:        toolSearch,
			Description: "Search the web for recent information. Read-only. Use it to verify facts or find fresh developments.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Search query"},
				},
				"required": []string{"query"},
			},
		})
	}
	tools = append(tools, llm.Tool{
		Name:        toolSubmit,
		Description: "Submit the finished drafts for human review. Call once, with one draft per requested platform.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"drafts": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"platform":     map[string]any{"type": "string", "enum": platforms},
							"title":        map[string]any{"type": "string", "description": "Short internal title for the reviewer"},
							"content":      map[string]any{"type": "string", "description": "Post body (plain text, no hashtags)"},
							"thread_parts": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "For thread-capable platforms: ordered parts, each within the platform limit. Empty for a single post."},
							"hashtags":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Without '#'"},
							"image_prompt": map[string]any{"type": "string", "description": "Prompt for an accompanying image (required for Instagram)"},
							"source_refs":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Refs of the material used, e.g. S1"},
						},
						"required": []string{"platform", "title", "content"},
					},
				},
			},
			"required": []string{"drafts"},
		},
	})
	return tools
}

// Run executes the tool loop until the model submits drafts or the turn budget runs out.
func (g *Generator) Run(ctx context.Context, in Input) (*Result, error) {
	ctx = guard.WithAgent(ctx)
	maxTurns := g.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 8
	}
	maxSearches := g.MaxSearches
	if maxSearches <= 0 {
		maxSearches = 5
	}
	in.CanSearch = g.Search != nil
	tools := g.tools(in.CanSearch, in.Platforms)
	allowed := map[string]bool{}
	for _, t := range tools {
		allowed[t.Name] = true
	}

	msgs := []llm.Message{{Role: llm.RoleUser, Content: buildUserPrompt(in, g.MaxContentChar)}}
	res := &Result{}
	searches := 0
	nudged := false

	for turn := 0; turn < maxTurns; turn++ {
		resp, err := g.LLM.Chat(ctx, llm.Request{System: systemPrompt, Messages: msgs, Tools: tools})
		if err != nil {
			return res, err
		}
		res.Usage.InputTokens += resp.Usage.InputTokens
		res.Usage.OutputTokens += resp.Usage.OutputTokens
		msgs = append(msgs, resp.Message)

		if len(resp.Message.ToolCalls) == 0 {
			// Some local models answer in text despite the instruction; salvage JSON if present.
			if drafts, ok := draftsFromText(resp.Message.Content); ok {
				res.Drafts = normalizeDrafts(drafts, in.Platforms)
				if len(res.Drafts) > 0 {
					return res, nil
				}
			}
			if resp.StopReason == "max_tokens" || nudged {
				return res, ErrNoDrafts
			}
			nudged = true
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: "Please call the submit_drafts tool now with your drafts."})
			continue
		}

		var submitted []Draft
		submittedOK := false
		for _, call := range resp.Message.ToolCalls {
			res.ToolCalls++
			out, isErr := g.execTool(ctx, call, allowed, &searches, maxSearches, res, &submitted, &submittedOK)
			msgs = append(msgs, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Content: out, IsError: isErr})
		}
		if submittedOK {
			res.Drafts = normalizeDrafts(submitted, in.Platforms)
			if len(res.Drafts) == 0 {
				return res, ErrNoDrafts
			}
			return res, nil
		}
	}
	return res, fmt.Errorf("%w (turn limit %d reached)", ErrNoDrafts, maxTurns)
}

func (g *Generator) execTool(ctx context.Context, call llm.ToolCall, allowed map[string]bool, searches *int, maxSearches int,
	res *Result, submitted *[]Draft, ok *bool) (string, bool) {
	if !allowed[call.Name] {
		g.logf("warn", "blocked tool call %q (not permitted for the agent)", call.Name)
		return fmt.Sprintf("Tool %q is not permitted. Publishing is done by humans after review.", call.Name), true
	}
	switch call.Name {
	case toolSearch:
		var args struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil || strings.TrimSpace(args.Query) == "" {
			return "invalid arguments: query is required", true
		}
		if *searches >= maxSearches {
			return "search limit reached; write the drafts with what you have", true
		}
		*searches++
		results, err := g.Search.Search(ctx, args.Query, 5)
		if err != nil {
			g.logf("warn", "web_search %q failed: %v", args.Query, err)
			return "search failed: " + err.Error(), true
		}
		g.logf("info", "web_search %q: %d results", args.Query, len(results))
		res.SearchResults = append(res.SearchResults, results...)
		raw, _ := json.Marshal(results)
		return string(raw), false
	case toolSubmit:
		var args struct {
			Drafts []Draft `json:"drafts"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return "invalid arguments: " + err.Error(), true
		}
		if len(args.Drafts) == 0 {
			return "no drafts provided", true
		}
		*submitted = append(*submitted, args.Drafts...)
		*ok = true
		return "Drafts received and queued for human review.", false
	}
	return "unknown tool", true
}

var jsonBlock = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```|(\\{.*\\})")

func draftsFromText(text string) ([]Draft, bool) {
	m := jsonBlock.FindStringSubmatch(text)
	if m == nil {
		return nil, false
	}
	raw := m[1]
	if raw == "" {
		raw = m[2]
	}
	var args struct {
		Drafts []Draft `json:"drafts"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil || len(args.Drafts) == 0 {
		return nil, false
	}
	return args.Drafts, true
}

// normalizeDrafts keeps drafts for requested platforms (first per platform), enforces length limits
// by splitting threads, and strips stray hashtags/markdown.
func normalizeDrafts(drafts []Draft, platforms []string) []Draft {
	want := map[string]bool{}
	for _, p := range platforms {
		want[p] = true
	}
	seen := map[string]bool{}
	var out []Draft
	for _, d := range drafts {
		d.Platform = strings.ToLower(strings.TrimSpace(d.Platform))
		if !want[d.Platform] || seen[d.Platform] {
			continue
		}
		pl, _ := domain.PlatformByID(d.Platform)
		d.Content = cleanText(d.Content)
		parts := make([]string, 0, len(d.ThreadParts)+1)
		for _, p := range d.ThreadParts {
			if p = cleanText(p); p != "" {
				parts = append(parts, p)
			}
		}
		// Some models put the first part in content and only the replies in thread_parts. Keep it as the
		// first part instead of dropping it, unless content is just the thread again.
		if all := strings.Join(parts, "\n\n"); len(parts) > 0 && d.Content != "" && !strings.Contains(all, d.Content) {
			parts = append([]string{d.Content}, parts...)
		}
		if pl.SupportsThread {
			if len(parts) == 0 && runeLen(d.Content) > pl.MaxChars {
				parts = textutil.SplitThread(d.Content, pl.MaxChars)
			} else if len(parts) > 0 {
				parts = textutil.FitParts(parts, pl.MaxChars)
			}
			if len(parts) == 1 {
				d.Content, parts = parts[0], nil
			}
		} else if len(parts) > 0 {
			if d.Content == "" {
				d.Content = strings.Join(parts, "\n\n")
			}
			parts = nil
		}
		if len(parts) > 0 {
			d.Content = strings.Join(parts, "\n\n")
		}
		d.ThreadParts = parts
		d.Hashtags = cleanTags(d.Hashtags, pl.MaxHashtags)
		if d.Content == "" {
			continue
		}
		if strings.TrimSpace(d.Title) == "" {
			d.Title = clip(oneLine(d.Content), 60)
		}
		seen[d.Platform] = true
		out = append(out, d)
	}
	return out
}

var mdMarkers = strings.NewReplacer("**", "", "__", "", "### ", "", "## ", "", "# ", "")

func cleanText(s string) string {
	return strings.TrimSpace(mdMarkers.Replace(s))
}

func cleanTags(tags []string, max int) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.ReplaceAll(strings.TrimLeft(strings.TrimSpace(t), "#"), " ", "")
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func runeLen(s string) int { return len([]rune(s)) }

// Normalize applies the same cleanup and length enforcement used for pipeline drafts
// (thread splitting, hashtag caps, markdown stripping) to drafts produced elsewhere.
func Normalize(drafts []Draft, platforms []string) []Draft { return normalizeDrafts(drafts, platforms) }
