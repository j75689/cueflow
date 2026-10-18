package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/guard"
	"github.com/j75689/cueflow/internal/llm"
)

// scriptedLLM replays canned assistant turns and records what it was sent.
type scriptedLLM struct {
	turns    []llm.Message
	requests []llm.Request
	ctxs     []context.Context
}

func (s *scriptedLLM) Model() string { return "scripted" }

func (s *scriptedLLM) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	s.requests = append(s.requests, req)
	s.ctxs = append(s.ctxs, ctx)
	msg := s.turns[0]
	s.turns = s.turns[1:]
	stop := "end"
	if len(msg.ToolCalls) > 0 {
		stop = "tool_use"
	}
	return &llm.Response{Message: msg, StopReason: stop}, nil
}

type fakeSearch struct{ queries []string }

func (f *fakeSearch) Test(context.Context) (*connector.TestResult, error) { return nil, nil }
func (f *fakeSearch) Search(_ context.Context, q string, _ int) ([]connector.SearchResult, error) {
	f.queries = append(f.queries, q)
	return []connector.SearchResult{{Title: "Result", URL: "https://example.com/r"}}, nil
}

func call(id, name string, args any) llm.ToolCall {
	raw, _ := json.Marshal(args)
	return llm.ToolCall{ID: id, Name: name, Arguments: raw}
}

func TestAgentLoopBlocksPublishingAndSubmitsDrafts(t *testing.T) {
	long := strings.Repeat("Agent tooling keeps moving from chat boxes to ops consoles. ", 20) // > 500 runes, no thread_parts
	model := &scriptedLLM{turns: []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			call("1", "integrationSchedulePostTool", map[string]any{"socialPost": []any{}}),
			call("2", "web_search", map[string]any{"query": "agent ops"}),
		}},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call("3", "submit_drafts", map[string]any{"drafts": []map[string]any{
			{"platform": "threads", "title": "T", "content": long, "hashtags": []string{"#AI", "ai", "agents"}, "source_refs": []string{"S1"}},
			{"platform": "instagram", "title": "I", "content": "**Bold** caption", "hashtags": []string{"a", "b"}, "image_prompt": "a robot"},
			{"platform": "linkedin", "title": "not requested", "content": "x"},
		}})}},
	}}
	search := &fakeSearch{}
	var logs []string
	g := &Generator{LLM: model, Search: search, Logf: func(l, m string) { logs = append(logs, l+": "+m) }}
	res, err := g.Run(context.Background(), Input{
		Topic: "AI agents", Language: "zh-TW", Platforms: []string{"threads", "instagram"},
		Materials: []Material{{Ref: "S1", Title: "News", URL: "https://example.com", Content: "body"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The blocked tool got an error result, the search ran.
	second := model.requests[1].Messages
	var blocked *llm.Message
	for i := range second {
		if second[i].Role == llm.RoleTool && second[i].ToolCallID == "1" {
			blocked = &second[i]
		}
	}
	if blocked == nil || !blocked.IsError || !strings.Contains(blocked.Content, "not permitted") {
		t.Fatalf("publishing tool was not blocked: %+v", blocked)
	}
	if len(search.queries) != 1 {
		t.Fatalf("search not executed: %v", search.queries)
	}
	for _, c := range model.ctxs {
		if !guard.InAgent(c) {
			t.Fatal("LLM calls must run in an agent-marked context")
		}
	}
	// Only the two allowed tools are advertised.
	var names []string
	for _, tl := range model.requests[0].Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "web_search,submit_drafts" {
		t.Fatalf("advertised tools: %v", names)
	}

	if len(res.Drafts) != 2 {
		t.Fatalf("want 2 drafts (linkedin dropped), got %d", len(res.Drafts))
	}
	th := res.Drafts[0]
	if th.Platform != "threads" || len(th.ThreadParts) < 2 {
		t.Fatalf("long threads draft should be split: %+v", th)
	}
	for _, p := range th.ThreadParts {
		if utf8.RuneCountInString(p) > 500 {
			t.Errorf("thread part over limit: %d", utf8.RuneCountInString(p))
		}
	}
	if len(th.Hashtags) != 1 || th.Hashtags[0] != "AI" {
		t.Errorf("threads hashtags should be deduped and capped at 1: %v", th.Hashtags)
	}
	if ig := res.Drafts[1]; ig.Content != "Bold caption" || ig.ImagePrompt != "a robot" {
		t.Errorf("instagram draft not cleaned: %+v", ig)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "blocked tool call") {
		t.Errorf("blocked call not logged: %v", logs)
	}
}

func TestAgentNudgesOnceThenFails(t *testing.T) {
	model := &scriptedLLM{turns: []llm.Message{
		{Role: llm.RoleAssistant, Content: "Here are my thoughts"},
		{Role: llm.RoleAssistant, Content: "still no tool"},
	}}
	_, err := (&Generator{LLM: model}).Run(context.Background(), Input{Platforms: []string{"threads"}})
	if err == nil {
		t.Fatal("expected ErrNoDrafts")
	}
	if len(model.requests) != 2 {
		t.Fatalf("expected one nudge, got %d requests", len(model.requests))
	}
}

func TestAgentSalvagesJSONText(t *testing.T) {
	model := &scriptedLLM{turns: []llm.Message{
		{Role: llm.RoleAssistant, Content: "```json\n{\"drafts\":[{\"platform\":\"threads\",\"title\":\"t\",\"content\":\"hello\"}]}\n```"},
	}}
	res, err := (&Generator{LLM: model}).Run(context.Background(), Input{Platforms: []string{"threads"}})
	if err != nil || len(res.Drafts) != 1 || res.Drafts[0].Content != "hello" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestNoSearchToolWithoutSearcher(t *testing.T) {
	model := &scriptedLLM{turns: []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call("1", "web_search", map[string]any{"query": "x"})}},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call("2", "submit_drafts", map[string]any{"drafts": []map[string]any{
			{"platform": "x", "title": "t", "content": "hi"}}})}},
	}}
	res, err := (&Generator{LLM: model}).Run(context.Background(), Input{Platforms: []string{"x"}})
	if err != nil || len(res.Drafts) != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	for _, m := range model.requests[1].Messages {
		if m.ToolCallID == "1" && !m.IsError {
			t.Fatal("web_search must be rejected when no search connector is configured")
		}
	}
}
