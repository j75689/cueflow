package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/connectors/anthropic"
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
		writeMessage(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",
			"content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","stop_sequence":null,
			"usage":{"input_tokens":3,"output_tokens":1}}`)
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

// The append-only history is cached: a breakpoint on the system prompt (after the tools) and one on
// each of the two newest user turns. Replayed assistant turns get none, and the turn stored in
// ProviderState is left untouched. Usage counts the cached part of the prompt.
func TestPromptCaching(t *testing.T) {
	isolateEnv(t)
	srv, last := fakeMessages(t)
	inst, err := connector.Build("anthropic", connector.Config{"api_key": "sk-test", "base_url": srv.URL}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	stored := sdk.BetaMessageParam{Role: sdk.BetaMessageParamRoleAssistant, Content: []sdk.BetaContentBlockParamUnion{
		sdk.NewBetaThinkingBlock("sig", "plan"), sdk.NewBetaToolUseBlock("t1", map[string]any{}, "read"),
	}}
	_, err = inst.(connector.LLM).Chat(context.Background(), llm.Request{
		System: "sys",
		Tools:  []llm.Tool{{Name: "read", Description: "d", Schema: map[string]any{"type": "object"}}},
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "brief"},
			{Role: llm.RoleAssistant, ProviderState: stored},
			{Role: llm.RoleTool, ToolCallID: "t1", Content: "a"},
			{Role: llm.RoleTool, ToolCallID: "t2", Content: "b"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := last().body
	hasCache := func(v any) bool { _, ok := v.(map[string]any)["cache_control"]; return ok }
	if sys := body["system"].([]any); !hasCache(sys[0]) {
		t.Fatalf("system prompt not cached: %v", sys)
	}
	if tools := body["tools"].([]any); hasCache(tools[0]) {
		t.Fatal("the system breakpoint already covers the tools")
	}
	sent := body["messages"].([]any)
	content := func(i int) []any { return sent[i].(map[string]any)["content"].([]any) }
	if newest := content(2); !hasCache(newest[1]) || hasCache(newest[0]) {
		t.Fatalf("want one breakpoint on the newest turn's last block: %v", newest)
	}
	if !hasCache(content(0)[0]) {
		t.Fatal("the previous user turn should keep a breakpoint")
	}
	for _, b := range content(1) {
		if hasCache(b) {
			t.Fatal("a replayed assistant turn carries a breakpoint")
		}
	}
	if stored.Content[1].GetCacheControl().Type != "" {
		t.Fatal("the stored assistant turn was changed")
	}

	// Without a system prompt the fixed-prefix breakpoint goes on the last tool.
	_, err = inst.(connector.LLM).Chat(context.Background(), llm.Request{
		Tools:    []llm.Tool{{Name: "a", Schema: map[string]any{"type": "object"}}, {Name: "b", Schema: map[string]any{"type": "object"}}},
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body = last().body
	if tools := body["tools"].([]any); hasCache(tools[0]) || !hasCache(tools[1]) {
		t.Fatalf("want the breakpoint on the last tool only: %v", tools)
	}
	if body["system"] != nil {
		t.Fatal("no system prompt was given")
	}
}

func TestUsageCountsCachedInput(t *testing.T) {
	isolateEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeMessage(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",
			"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,
			"usage":{"input_tokens":2,"cache_read_input_tokens":90000,"cache_creation_input_tokens":4000,"output_tokens":7}}`)
	}))
	t.Cleanup(srv.Close)
	inst, err := connector.Build("anthropic", connector.Config{"api_key": "sk-test", "base_url": srv.URL}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := inst.(connector.LLM).Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := (llm.Usage{InputTokens: 94002, OutputTokens: 7, CacheReadTokens: 90000, CacheWriteTokens: 4000}); resp.Usage != want {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

// With prompt caching turned off, no cache_control is sent anywhere.
func TestPromptCachingOff(t *testing.T) {
	isolateEnv(t)
	srv, last := fakeMessages(t)
	inst, err := connector.Build("anthropic", connector.Config{"api_key": "sk-test", "base_url": srv.URL, "prompt_caching": false}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst.(connector.LLM).Chat(context.Background(), llm.Request{System: "sys",
		Tools:    []llm.Tool{{Name: "a", Schema: map[string]any{"type": "object"}}},
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(last().body); strings.Contains(string(raw), "cache_control") {
		t.Fatalf("cache_control sent: %s", raw)
	}
}

// An endpoint that rejects cache_control gets the request again without it, and later requests skip
// it: caching is a saving, not a reason for every run to fail.
func TestPromptCachingRejectedFallsBack(t *testing.T) {
	isolateEnv(t)
	var mu sync.Mutex
	var withCache []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		cached := strings.Contains(string(raw), "cache_control")
		mu.Lock()
		withCache = append(withCache, cached)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if cached {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"system.0.cache_control: Extra inputs are not permitted"}}`))
			return
		}
		writeMessage(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",
			"content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","stop_sequence":null,
			"usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	inst, err := connector.Build("anthropic", connector.Config{"api_key": "sk-test", "base_url": srv.URL}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		resp, err := inst.(connector.LLM).Chat(context.Background(), llm.Request{System: "sys", Messages: []llm.Message{{Role: llm.RoleUser, Content: "ping"}}})
		if err != nil || resp.Message.Content != "pong" {
			t.Fatalf("resp %v, err %v", resp, err)
		}
	}
	if len(withCache) != 3 || !withCache[0] || withCache[1] || withCache[2] {
		t.Fatalf("want one refused cached request, then two without cache_control: %v", withCache)
	}
}

// The Claude API's (prompt too long, or prompt plus max_tokens too long) and Bedrock's refusals come
// back as llm.ErrContextOverflow; other 400s don't.
func TestContextOverflowRecognized(t *testing.T) {
	isolateEnv(t)
	for _, c := range []struct {
		body string
		want bool
	}{
		{`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1048213 tokens > 1000000 maximum"}}`, true},
		{`{"type":"error","error":{"type":"invalid_request_error","message":"input length and max_tokens exceed context limit: 990000 + 16000 > 1000000, decrease input length or max_tokens and try again"}}`, true},
		{`{"message":"Input is too long for requested model."}`, true},
		{`{"type":"error","error":{"type":"invalid_request_error","message":"tools.0.name: invalid"}}`, false},
		// A gateway's own error page is not the API's body, even if it says something similar.
		{`<html><body>400: input is too long for this proxy</body></html>`, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(c.body))
		}))
		inst, err := connector.Build("anthropic", connector.Config{"api_key": "sk-test", "base_url": srv.URL}, connector.Env{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = inst.(connector.LLM).Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
		srv.Close()
		if got := errors.Is(err, llm.ErrContextOverflow); got != c.want {
			t.Errorf("%s: overflow=%v, want %v (%v)", c.body, got, c.want, err)
		}
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

// The context window comes from the Models API once per model and process (not per run), a
// configured value wins, and a failed lookup is 0 (no percentage) and isn't retried at once.
func TestContextWindow(t *testing.T) {
	isolateEnv(t)
	var lookups atomic.Int32
	fail := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/models/") {
			http.NotFound(w, r)
			return
		}
		lookups.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"down"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"type":"model","id":"claude-opus-5-5","display_name":"Opus","created_at":"2026-01-01T00:00:00Z",
			"max_input_tokens":1000000,"max_tokens":128000}`))
	}))
	t.Cleanup(srv.Close)
	window := func(cfg connector.Config) int64 {
		inst, err := connector.Build("anthropic", cfg, connector.Env{})
		if err != nil {
			t.Fatal(err)
		}
		return inst.(llm.ContextWindower).ContextWindow(context.Background())
	}
	cfg := connector.Config{"api_key": "sk-test", "base_url": srv.URL}
	if w := window(cfg); w != 1000000 {
		t.Fatalf("window: %d", w)
	}
	if w := window(cfg); w != 1000000 || lookups.Load() != 1 {
		t.Fatalf("a second instance should use the cache: %d after %d lookups", w, lookups.Load())
	}
	if w := window(connector.Config{"api_key": "sk-test", "base_url": srv.URL, "context_window": 200000}); w != 200000 || lookups.Load() != 1 {
		t.Fatalf("configured window: %d", w)
	}
	fail.Store(true)
	other := connector.Config{"api_key": "sk-test", "base_url": srv.URL, "model": "claude-sonnet-5-5"}
	if w := window(other); w != 0 {
		t.Fatalf("a failed lookup should be unknown: %d", w)
	}
	before := lookups.Load()
	if w := window(other); w != 0 || lookups.Load() != before {
		t.Fatal("a failed lookup was retried at once")
	}
}

// The lookup sends the OAuth beta an auth_token needs, concurrent first runs share one request, a
// cancelled run doesn't record a failure for the others, a timeout isn't remembered, and a window
// that isn't positive is unknown.
func TestContextWindowLookupRobustness(t *testing.T) {
	isolateEnv(t)
	var lookups atomic.Int32
	slow := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		if slow.Load() {
			time.Sleep(300 * time.Millisecond)
		} else {
			time.Sleep(50 * time.Millisecond) // long enough for concurrent callers to overlap
		}
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(strings.Join(r.Header.Values("Anthropic-Beta"), ","), "oauth-2025-04-20") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"OAuth needs the beta"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"type":"model","id":"m","display_name":"m","created_at":"2026-01-01T00:00:00Z","max_input_tokens":500000,"max_tokens":1}`))
	}))
	t.Cleanup(srv.Close)
	build := func(cfg connector.Config) llm.ContextWindower {
		inst, err := connector.Build("anthropic", cfg, connector.Env{})
		if err != nil {
			t.Fatal(err)
		}
		return inst.(llm.ContextWindower)
	}
	cfg := func(model string) connector.Config {
		return connector.Config{"auth_token": "oauth-tok", "base_url": srv.URL, "model": model}
	}

	// A run cancelled before it asks: the lookup isn't cancelled with it, so the answer is recorded
	// for everyone (not a failure), and the next run doesn't ask again.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if w := build(cfg("a")).ContextWindow(cancelled); w != 500000 {
		t.Fatalf("a cancelled caller's lookup should still complete (the OAuth beta must be sent): %d", w)
	}
	if w := build(cfg("a")).ContextWindow(context.Background()); w != 500000 || lookups.Load() != 1 {
		t.Fatalf("after a cancelled caller: %d with %d lookups", w, lookups.Load())
	}
	// A run that gives up while another run's lookup is in flight gets 0.
	slow.Store(true)
	go build(cfg("w")).ContextWindow(context.Background())
	time.Sleep(20 * time.Millisecond)
	short, cancelShort := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelShort()
	if w := build(cfg("w")).ContextWindow(short); w != 0 {
		t.Fatalf("a waiter that gives up: %d", w)
	}
	slow.Store(false)
	time.Sleep(400 * time.Millisecond) // let that lookup finish before counting
	base := lookups.Load()

	// Ten runs starting at once send one request.
	var wg sync.WaitGroup
	got := make([]int64, 10)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i] = build(cfg("b")).ContextWindow(context.Background())
		}()
	}
	wg.Wait()
	for _, w := range got {
		if w != 500000 {
			t.Fatalf("concurrent windows: %v", got)
		}
	}
	if n := lookups.Load(); n != base+1 {
		t.Fatalf("concurrent first runs sent %d lookups, want 1", n-base)
	}

	// A timeout is unknown now but asked again next time.
	defer anthropic.SetWindowTimeout(100 * time.Millisecond)()
	slow.Store(true)
	if w := build(cfg("c")).ContextWindow(context.Background()); w != 0 {
		t.Fatalf("a timed-out lookup: %d", w)
	}
	slow.Store(false)
	if w := build(cfg("c")).ContextWindow(context.Background()); w != 500000 {
		t.Fatalf("after a timeout the next run should ask again: %d", w)
	}

	// A negative setting is unknown, not a negative percentage, and asks nothing.
	before := lookups.Load()
	inst, _ := connector.Build("anthropic_bedrock", connector.Config{"region": "us-east-1", "api_key": "k", "context_window": -5}, connector.Env{})
	if w := inst.(llm.ContextWindower).ContextWindow(context.Background()); w != 0 || lookups.Load() != before {
		t.Fatalf("negative window: %d", w)
	}
}

// A user message right after tool results (the crew's hand-off request) is sent in the same user
// turn as the results, so turns still alternate.
func TestUserMessageAfterToolResultsJoinsTheirTurn(t *testing.T) {
	isolateEnv(t)
	srv, last := fakeMessages(t)
	inst, err := connector.Build("anthropic", connector.Config{"api_key": "sk-test", "base_url": srv.URL}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst.(connector.LLM).Chat(context.Background(), llm.Request{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: "brief"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "t1", Name: "read", Arguments: json.RawMessage(`{}`)}}},
		{Role: llm.RoleTool, ToolCallID: "t1", Content: "result"},
		{Role: llm.RoleUser, Content: "write the hand-off note"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	sent := last().body["messages"].([]any)
	if len(sent) != 3 {
		t.Fatalf("want brief, assistant, one user turn; got %d messages", len(sent))
	}
	blocks := sent[2].(map[string]any)["content"].([]any)
	if len(blocks) != 2 || blocks[0].(map[string]any)["type"] != "tool_result" || blocks[1].(map[string]any)["text"] != "write the hand-off note" {
		t.Fatalf("last turn: %v", blocks)
	}
}

// writeMessage answers a streaming Messages request with msg (a whole message, as the non-streaming
// API returns it) as the server would stream it: message_start, a start and stop per content block,
// message_delta and message_stop.
func writeMessage(w http.ResponseWriter, msg string) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(msg), &m); err != nil {
		panic(err)
	}
	var blocks []json.RawMessage
	_ = json.Unmarshal(m["content"], &blocks)
	m["content"] = json.RawMessage(`[]`)
	start, _ := json.Marshal(m)
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(name, data string) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data) }
	event("message_start", `{"type":"message_start","message":`+string(start)+`}`)
	for i, b := range blocks {
		event("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":%s}`, i, b))
		event("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, i))
	}
	event("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%s,"stop_sequence":null},"usage":%s}`, m["stop_reason"], m["usage"]))
	event("message_stop", `{"type":"message_stop"}`)
}

// A long answer streams in pieces (text and tool input deltas) and is put back together, and the
// shared HTTP client's overall timeout doesn't cut it off: what made a run writing a long document
// fail with "Client.Timeout exceeded while awaiting headers".
func TestStreamedAnswerOutlivesTheSharedClientTimeout(t *testing.T) {
	isolateEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"stream":true`) {
			t.Errorf("request isn't streamed: %s", raw)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flush := w.(http.Flusher).Flush
		event := func(name, data string) {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
			flush()
			time.Sleep(150 * time.Millisecond) // 6 events: 900 ms in all, past the 300 ms timeout
		}
		event("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"cache_read_input_tokens":3,"output_tokens":1}}}`)
		event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Writing the "}}`)
		event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"plan."}}`)
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprint(w, `event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"write_file","input":{}}}`+"\n\n")
		event("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"name\":\"plan.md\","}}`)
		fmt.Fprint(w, `event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"content\":\"x\"}"}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
		event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":40}}`)
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	inst, err := connector.Build("anthropic", connector.Config{"api_key": "sk-test", "base_url": srv.URL, "refusal_fallback": false},
		connector.Env{HTTPClient: &http.Client{Timeout: 300 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := inst.(connector.LLM).Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "go"}}})
	if err != nil {
		t.Fatalf("a long stream was cut off: %v", err)
	}
	if resp.Message.Content != "Writing the plan." || resp.StopReason != "tool_use" || len(resp.Message.ToolCalls) != 1 ||
		string(resp.Message.ToolCalls[0].Arguments) != `{"name":"plan.md","content":"x"}` {
		t.Fatalf("reassembled: %+v", resp)
	}
	if resp.Usage.InputTokens != 15 || resp.Usage.OutputTokens != 40 || resp.Usage.CacheReadTokens != 3 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}
