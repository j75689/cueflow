package openai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/connector"
	_ "github.com/j75689/cueflow/internal/connectors/openai"
	"github.com/j75689/cueflow/internal/llm"
)

func TestChatWithToolCalls(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("no key configured, but Authorization was sent")
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,
			"tool_calls":[{"id":"c1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"go\"}"}}]}}],
			"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer srv.Close()

	inst, err := connector.Build("openai", connector.Config{"provider": "ollama", "base_url": srv.URL + "/v1", "model": "qwen3"}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	model := inst.(connector.LLM)
	resp, err := model.Chat(context.Background(), llm.Request{
		System: "sys",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "hi"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c0", Name: "web_search", Arguments: json.RawMessage(`{"query":"a"}`)}}},
			{Role: llm.RoleTool, ToolCallID: "c0", Content: "[]"},
		},
		Tools: []llm.Tool{{Name: "web_search", Description: "d", Schema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" || len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "web_search" {
		t.Fatalf("resp: %+v", resp)
	}
	if string(resp.Message.ToolCalls[0].Arguments) != `{"query":"go"}` {
		t.Errorf("args: %s", resp.Message.ToolCalls[0].Arguments)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Errorf("usage: %+v", resp.Usage)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("messages sent: %v", msgs)
	}
	if got["model"] != "qwen3" || got["max_tokens"] == nil || got["tools"] == nil {
		t.Errorf("request body: %v", got)
	}
}

// A tool-history budget cuts the oldest tool results before sending (these servers don't check
// earlier turns); the newest result and the caller's history stay whole.
func TestToolHistoryBudgetCutsOldResults(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()
	inst, err := connector.Build("openai", connector.Config{"provider": "ollama", "base_url": srv.URL + "/v1", "model": "qwen3"}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 40000)
	call := func(id string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "read", Arguments: json.RawMessage(`{}`)}}}
	}
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "brief"},
		call("c0"), {Role: llm.RoleTool, ToolCallID: "c0", Content: long},
		call("c1"), {Role: llm.RoleTool, ToolCallID: "c1", Content: long},
	}
	if _, err := inst.(connector.LLM).Chat(context.Background(), llm.Request{Messages: msgs, ToolHistoryBudget: 64000}); err != nil {
		t.Fatal(err)
	}
	sent := got["messages"].([]any)
	if c := sent[2].(map[string]any)["content"].(string); !strings.Contains(c, "cut from the history") || len(c) >= len(long) {
		t.Fatalf("old result not cut: %d characters", len(c))
	}
	if sent[4].(map[string]any)["content"] != long || msgs[2].Content != long {
		t.Fatal("cut the newest result or the caller's history")
	}
}

func TestHTTPErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	inst, _ := connector.Build("openai", connector.Config{"provider": "openai_compatible", "base_url": srv.URL, "model": "m", "api_key": "k"}, connector.Env{})
	res, _ := inst.Test(context.Background())
	if res.OK {
		t.Fatal("expected test failure")
	}
}

func TestRequiresModelForCustomProvider(t *testing.T) {
	if _, err := connector.Build("openai", connector.Config{"provider": "openai_compatible", "base_url": "http://x"}, connector.Env{}); err == nil {
		t.Fatal("expected error when model is missing")
	}
}

type seen struct {
	path, query string
	header      http.Header
	body        map[string]any
}

func recorder(t *testing.T) (*httptest.Server, *seen) {
	t.Helper()
	got := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.query, got.header = r.URL.Path, r.URL.RawQuery, r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"pong"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func ping(t *testing.T, cfg connector.Config) {
	t.Helper()
	inst, err := connector.Build("openai", cfg, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.(connector.LLM).Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "ping"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRouter(t *testing.T) {
	srv, got := recorder(t)
	ping(t, connector.Config{"provider": "openrouter", "base_url": srv.URL + "/api/v1", "api_key": "sk-or-v1-test"})
	if got.path != "/api/v1/chat/completions" || got.header.Get("Authorization") != "Bearer sk-or-v1-test" {
		t.Fatalf("request: %s %v", got.path, got.header)
	}
	if got.header.Get("X-Title") != "CueFlow" || got.header.Get("HTTP-Referer") == "" {
		t.Fatalf("attribution headers missing: %v", got.header)
	}
	if got.body["model"] != "openrouter/auto" {
		t.Fatalf("default model: %v", got.body["model"])
	}
	// Defaults: real OpenRouter base URL.
	inst, _ := connector.Build("openai", connector.Config{"provider": "openrouter", "api_key": "k"}, connector.Env{})
	if inst == nil {
		t.Fatal("openrouter preset should build without a base URL")
	}
}

func TestCustomHeadersOverride(t *testing.T) {
	srv, got := recorder(t)
	ping(t, connector.Config{"provider": "openrouter", "base_url": srv.URL, "api_key": "k",
		"headers": map[string]any{"X-Title": "My Studio", "X-Gateway-Key": "gw"}})
	if got.header.Get("X-Title") != "My Studio" || got.header.Get("X-Gateway-Key") != "gw" {
		t.Fatalf("headers: %v", got.header)
	}
}

func TestAzureV1(t *testing.T) {
	srv, got := recorder(t)
	ping(t, connector.Config{"provider": "azure", "base_url": srv.URL + "/openai/v1", "api_key": "az-key", "model": "my-gpt"})
	if got.path != "/openai/v1/chat/completions" || got.header.Get("Api-Key") != "az-key" || got.header.Get("Authorization") != "" {
		t.Fatalf("request: %s %v", got.path, got.header)
	}
	if got.body["max_completion_tokens"] == nil || got.body["model"] != "my-gpt" {
		t.Fatalf("body: %v", got.body)
	}
}

func TestAzureLegacyDeployments(t *testing.T) {
	srv, got := recorder(t)
	ping(t, connector.Config{"provider": "azure", "base_url": srv.URL + "/openai", "api_key": "az-key",
		"model": "prod-gpt", "api_version": "2024-10-21"})
	if got.path != "/openai/deployments/prod-gpt/chat/completions" || got.query != "api-version=2024-10-21" {
		t.Fatalf("url: %s?%s", got.path, got.query)
	}
}

func TestAzureRequiresKeyAndDeployment(t *testing.T) {
	if _, err := connector.Build("openai", connector.Config{"provider": "azure", "base_url": "https://x/openai/v1", "model": "m"}, connector.Env{}); err == nil {
		t.Fatal("azure without a key should fail")
	}
	if _, err := connector.Build("openai", connector.Config{"provider": "azure", "base_url": "https://x/openai/v1", "api_key": "k"}, connector.Env{}); err == nil {
		t.Fatal("azure without a deployment name should fail")
	}
}
