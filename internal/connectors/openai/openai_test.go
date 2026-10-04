package openai_test

import (
	"context"
	"encoding/json"
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
