package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
			"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":4}}}`))
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
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 || resp.Usage.CacheReadTokens != 4 {
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

// A prompt longer than the model's window comes back as llm.ErrContextOverflow, so the crew can say so
// plainly; other errors, including ones that only look similar, keep their own text.
func TestContextOverflowRecognized(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 128000 tokens."}}`, true},
		{400, `{"error":{"code":400,"message":"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576)."}}`, true},
		// DeepSeek, vLLM and OpenRouter. (The same words for a max_tokens larger than the window are
		// told apart by the crew: that one fails on the first turn.)
		{400, `{"object":"error","message":"This model's maximum context length is 65536 tokens. However, you requested 70000 tokens (65000 in the messages, 5000 in the completion)."}`, true},
		// Ollama answers 500.
		{500, `{"error":"context length exceeded"}`, true},
		{500, `{"error":"internal error"}`, false},
		// A proxy's body limit: not an overflow.
		{413, `<html>413 Request Entity Too Large</html>`, false},
		{400, `{"error":"invalid tool schema"}`, false},
		{401, `{"error":"bad key"}`, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, c.body, c.status)
		}))
		inst, _ := connector.Build("openai", connector.Config{"provider": "openai_compatible", "base_url": srv.URL, "model": "m", "api_key": "k"}, connector.Env{})
		_, err := inst.(connector.LLM).Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
		srv.Close()
		if got := errors.Is(err, llm.ErrContextOverflow); got != c.want {
			t.Errorf("%d %s: overflow=%v, want %v (%v)", c.status, c.body, got, c.want, err)
		}
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

// The context window is what the operator configured; without one it is unknown (0), not a guess.
func TestContextWindowConfigured(t *testing.T) {
	for cfg, want := range map[string]int64{"": 0, "128000": 128000} {
		c := connector.Config{"provider": "ollama", "model": "qwen3"}
		if cfg != "" {
			c["context_window"] = cfg
		}
		inst, err := connector.Build("openai", c, connector.Env{})
		if err != nil {
			t.Fatal(err)
		}
		if got := inst.(llm.ContextWindower).ContextWindow(context.Background()); got != want {
			t.Errorf("context_window %q: %d", cfg, got)
		}
	}
}
