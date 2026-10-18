package content

import (
	"strings"

	"github.com/j75689/cueflow/internal/textutil"
)

// Draft is what an agent or the pipeline generator proposes for one platform, before it becomes a post.
type Draft struct {
	Platform    string   `json:"platform"`
	Title       string   `json:"title"`
	Content     string   `json:"content"`
	ThreadParts []string `json:"thread_parts"`
	Hashtags    []string `json:"hashtags"`
	ImagePrompt string   `json:"image_prompt"`
	SourceRefs  []string `json:"source_refs"`
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
		pl, _ := PlatformByID(d.Platform)
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
		d.Hashtags = cleanDraftTags(d.Hashtags, pl.MaxHashtags)
		if d.Content == "" {
			continue
		}
		if strings.TrimSpace(d.Title) == "" {
			d.Title = textutil.Clip(oneLine(d.Content), 60)
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

func cleanDraftTags(tags []string, max int) []string {
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

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
