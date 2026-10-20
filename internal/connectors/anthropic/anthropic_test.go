package anthropic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/j75689/cueflow/internal/connector"
	_ "github.com/j75689/cueflow/internal/connectors/anthropic"
	"github.com/j75689/cueflow/internal/llm"
)

type captured struct {
	path, query string
	header      http.Header
	body        map[string]any
}

// fakeMessages mimics POST /v1/messages and records the last request.
func fakeMessages(t *testing.T) (*httptest.Server, func() captured) {
	t.Helper()
	var mu sync.Mutex
	var last captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = captured{path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone()}
		_ = json.Unmarshal(raw, &last.body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",
			"content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","stop_sequence":null,
			"usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() captured { mu.Lock(); defer mu.Unlock(); return last }
}

// isolateEnv hides credentials from the developer's environment so the SDK only sees the config.
func isolateEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "ANTHROPIC_") || strings.HasPrefix(k, "AWS_") {
			t.Setenv(k, v) // registers restore
			_ = os.Unsetenv(k)
		}
	}
	t.Setenv("HOME", t.TempDir()) // no ~/.config/anthropic or ~/.aws profiles
}

func chat(t *testing.T, typ string, cfg connector.Config) {
	t.Helper()
	inst, err := connector.Build(typ, cfg, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := inst.(connector.LLM).Chat(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "ping"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "pong" {
		t.Fatalf("content: %q", resp.Message.Content)
	}
}

func betas(c captured) string { return strings.Join(c.header.Values("Anthropic-Beta"), ",") }

func TestAPIKey(t *testing.T) {
	isolateEnv(t)
	srv, last := fakeMessages(t)
	chat(t, "anthropic", connector.Config{"api_key": "sk-test", "base_url": srv.URL})
	c := last()
	if c.header.Get("X-Api-Key") != "sk-test" || c.header.Get("Authorization") != "" {
		t.Fatalf("headers: %v", c.header)
	}
	if c.path != "/v1/messages" {
		t.Fatalf("path: %s", c.path)
	}
	// Opus 5.5 gets the server-side refusal fallback on the Claude API by default.
	if !strings.Contains(betas(c), "server-side-fallback-2026-07-01") || c.body["fallbacks"] != "default" {
		t.Fatalf("fallback not requested: betas=%q fallbacks=%v", betas(c), c.body["fallbacks"])
	}
}

// A tool-history budget is handed to the API as context management. The client sends the earlier
// turns exactly as the model saw them: cutting a tool result would invalidate the thinking block
// after it.
func TestToolHistoryBudgetClearsServerSide(t *testing.T) {
	isolateEnv(t)
	srv, last := fakeMessages(t)
	inst, err := connector.Build("anthropic", connector.Config{"api_key": "sk-test", "base_url": srv.URL}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 40000)
	thought := sdk.BetaMessageParam{Role: sdk.BetaMessageParamRoleAssistant, Content: []sdk.BetaContentBlockParamUnion{
		sdk.NewBetaThinkingBlock("sig-1", "read the file"),
		sdk.NewBetaToolUseBlock("t1", map[string]any{}, "read"),
	}}
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "brief"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "t0", Name: "read", Arguments: json.RawMessage(`{}`)}}},
		{Role: llm.RoleTool, ToolCallID: "t0", Content: long},
		{Role: llm.RoleAssistant, ProviderState: thought},
		{Role: llm.RoleTool, ToolCallID: "t1", Content: long},
		{Role: llm.RoleTool, ToolCallID: "t2", Content: "a"},
		{Role: llm.RoleTool, ToolCallID: "t3", Content: "b"},
		{Role: llm.RoleTool, ToolCallID: "t4", Content: "c"},
	}
	if _, err := inst.(connector.LLM).Chat(context.Background(), llm.Request{Messages: msgs, ToolHistoryBudget: 64000}); err != nil {
		t.Fatal(err)
	}
	c := last()
	if !strings.Contains(betas(c), "context-management-2025-06-27") {
		t.Fatalf("context management beta missing: %q", betas(c))
	}
	edits, _ := c.body["context_management"].(map[string]any)["edits"].([]any)
	if len(edits) != 1 || edits[0].(map[string]any)["type"] != "clear_tool_uses_20250919" {
		t.Fatalf("context_management: %v", c.body["context_management"])
	}
	edit := edits[0].(map[string]any)
	// The newest turn called four tools: all four results are kept.
	if keep := edit["keep"].(map[string]any)["value"]; keep != 4.0 {
		t.Fatalf("keep: %v", keep)
	}
	// The trigger is the budget plus the rest of the prompt ("brief", the tool-call arguments), in tokens.
	if trig := edit["trigger"].(map[string]any)["value"]; trig != float64((len("brief")+len("{}")+64000)/3) {
		t.Fatalf("trigger: %v", trig)
	}
	sent := c.body["messages"].([]any)
	first := sent[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if text := first["content"].([]any)[0].(map[string]any)["text"]; first["type"] != "tool_result" || text != long {
		t.Fatal("an earlier tool result was changed before sending")
	}
	if b := sent[3].(map[string]any)["content"].([]any)[0].(map[string]any); b["type"] != "thinking" || b["signature"] != "sig-1" {
		t.Fatalf("thinking block not replayed: %v", b)
	}

	// Without a budget, nothing is cleared.
	chat(t, "anthropic", connector.Config{"api_key": "sk-test", "base_url": srv.URL})
	if c := last(); c.body["context_management"] != nil || strings.Contains(betas(c), "context-management") {
		t.Fatal("context management sent without a budget")
	}
}

// An endpoint that turns down context management (a gateway, or one that ignores it and lets the
// prompt grow too long) gets the history cut on the client instead, with the thinking blocks after
// the cut dropped so the API doesn't reject them. The connector stops asking for server clearing.
func TestToolHistoryFallsBackToClientCut(t *testing.T) {
	isolateEnv(t)
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if body["context_management"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"context_management: Extra inputs are not permitted"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",
			"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,
			"usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	inst, err := connector.Build("anthropic", connector.Config{"api_key": "sk-test", "base_url": srv.URL}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 40000)
	turn := func(id string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ProviderState: sdk.BetaMessageParam{Role: sdk.BetaMessageParamRoleAssistant,
			Content: []sdk.BetaContentBlockParamUnion{sdk.NewBetaThinkingBlock("sig-"+id, "think"), sdk.NewBetaToolUseBlock(id, map[string]any{}, "read")}}}
	}
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "brief"},
		turn("t0"), {Role: llm.RoleTool, ToolCallID: "t0", Content: long},
		turn("t1"), {Role: llm.RoleTool, ToolCallID: "t1", Content: long},
	}
	req := llm.Request{Messages: msgs, ToolHistoryBudget: 64000}
	for range 2 {
		if _, err := inst.(connector.LLM).Chat(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 3 || bodies[0]["context_management"] == nil || bodies[2]["context_management"] != nil {
		t.Fatalf("want one refused request, then two without context management; got %d requests", len(bodies))
	}
	sent := bodies[1]["messages"].([]any)
	blocks := func(i int) []any { return sent[i].(map[string]any)["content"].([]any) }
	if text := blocks(2)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(text, "cut from the history") {
		t.Fatal("the old result was not cut")
	}
	if blocks(1)[0].(map[string]any)["type"] != "thinking" {
		t.Fatal("the thinking before the cut should stay")
	}
	if b := blocks(3); len(b) != 1 || b[0].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("the thinking after the cut should be dropped: %v", b)
	}
	if msgs[2].Content != long {
		t.Fatal("the caller's history was changed")
	}
}

func TestOAuthTokenAddsBetaHeader(t *testing.T) {
	isolateEnv(t)
	srv, last := fakeMessages(t)
	chat(t, "anthropic", connector.Config{"auth_token": "oauth-tok", "base_url": srv.URL, "refusal_fallback": false})
	c := last()
	if c.header.Get("Authorization") != "Bearer oauth-tok" || c.header.Get("X-Api-Key") != "" {
		t.Fatalf("headers: %v", c.header)
	}
	if !strings.Contains(betas(c), "oauth-2025-04-20") {
		t.Fatalf("OAuth beta missing: %q", betas(c))
	}
	if _, ok := c.body["fallbacks"]; ok {
		t.Fatal("fallback disabled but still sent")
	}
}

func TestBearerTokenForGateways(t *testing.T) {
	isolateEnv(t)
	srv, last := fakeMessages(t)
	chat(t, "anthropic", connector.Config{"auth_token": "gw-tok", "auth_token_type": "bearer", "base_url": srv.URL})
	c := last()
	if c.header.Get("Authorization") != "Bearer gw-tok" || strings.Contains(betas(c), "oauth") {
		t.Fatalf("headers: %v", c.header)
	}
}

func TestAPIKeyWinsOverToken(t *testing.T) {
	isolateEnv(t)
	srv, last := fakeMessages(t)
	chat(t, "anthropic", connector.Config{"api_key": "sk-test", "auth_token": "tok", "base_url": srv.URL})
	if c := last(); c.header.Get("X-Api-Key") != "sk-test" || c.header.Get("Authorization") != "" {
		t.Fatalf("headers: %v", c.header)
	}
}

func TestServerEnvironmentFallback(t *testing.T) {
	isolateEnv(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-tok")
	srv, last := fakeMessages(t)
	chat(t, "anthropic", connector.Config{"base_url": srv.URL})
	if c := last(); c.header.Get("Authorization") != "Bearer env-tok" {
		t.Fatalf("env token not used: %v", c.header)
	}
}

func TestBedrockWithAPIKey(t *testing.T) {
	isolateEnv(t)
	srv, last := fakeMessages(t)
	chat(t, "anthropic_bedrock", connector.Config{"region": "us-east-1", "api_key": "bedrock-key", "base_url": srv.URL})
	c := last()
	// Bedrock API keys are bearer tokens.
	if c.header.Get("Authorization") != "Bearer bedrock-key" {
		t.Fatalf("headers: %v", c.header)
	}
	if c.header.Get("X-Stainless-Helper") != "fallback-refusal-middleware" {
		t.Fatalf("client-side refusal fallback middleware not installed: %v", c.header)
	}
	if !strings.HasSuffix(c.path, "/v1/messages") {
		t.Fatalf("path: %s", c.path)
	}
	if c.body["model"] != "anthropic.claude-opus-5-5" {
		t.Fatalf("model: %v", c.body["model"])
	}
	// No server-side routing outside the Claude API; the SDK middleware handles refusals instead.
	if _, ok := c.body["fallbacks"]; ok {
		t.Fatal("server-side fallback must not be sent to Bedrock")
	}
}

func TestBedrockWithAccessKeysSignsRequests(t *testing.T) {
	isolateEnv(t)
	srv, last := fakeMessages(t)
	chat(t, "anthropic_bedrock", connector.Config{"region": "us-west-2", "base_url": srv.URL,
		"aws_access_key_id": "AKIAEXAMPLE", "aws_secret_access_key": "secret"})
	c := last()
	if auth := c.header.Get("Authorization"); !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/") {
		t.Fatalf("request not SigV4-signed: %q", auth)
	}
}

func TestVertexValidation(t *testing.T) {
	isolateEnv(t)
	if _, err := connector.Build("anthropic_vertex", connector.Config{}, connector.Env{}); err == nil {
		t.Fatal("project_id is required")
	}
	_, err := connector.Build("anthropic_vertex", connector.Config{"project_id": "p", "service_account_json": "{not json"}, connector.Env{})
	if err == nil || !strings.Contains(err.Error(), "vertex: credentials") {
		t.Fatalf("bad key should fail clearly: %v", err)
	}
}
