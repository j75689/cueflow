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
