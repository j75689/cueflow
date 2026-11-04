package mcptools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/googleid"
)

type staticToken string

func (s staticToken) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: string(s)}, nil
}

func fakeIdentity(t *testing.T, src oauth2.TokenSource, err error) {
	t.Helper()
	old := googleid.NewTokenSource
	googleid.NewTokenSource = func(context.Context, string) (oauth2.TokenSource, error) { return src, err }
	t.Cleanup(func() { googleid.NewTokenSource = old })
}

// The HTTP client handed to the MCP transport signs requests with CueFlow's Google identity, so a
// private Cloud Run MCP server can be reached; the server's own bearer token still travels in
// Authorization (the connector adds that header separately, around this client).
func TestGoogleIdentityReachesTheTransportClient(t *testing.T) {
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	fakeIdentity(t, staticToken("tok-9"), nil)
	inst, err := New(connector.Config{"endpoint_url": srv.URL + "/mcp", "transport": "streamable", "google_identity": true,
		"auth_token": "apps-own"}, connector.Env{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	opts := inst.(*Server).opts
	if opts.Headers["Authorization"] != "Bearer apps-own" {
		t.Errorf("the bearer token stays in Authorization: %v", opts.Headers)
	}
	resp, err := opts.HTTPClient.Get(srv.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := seen.Get(googleid.Header); got != "Bearer tok-9" {
		t.Errorf("identity header = %q", got)
	}

	// Off by default: no identity header.
	seen = nil
	plain, err := New(connector.Config{"endpoint_url": srv.URL + "/mcp", "transport": "streamable"}, connector.Env{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	resp, err = plain.(*Server).opts.HTTPClient.Get(srv.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seen.Get(googleid.Header) != "" {
		t.Error("without google_identity no identity header may be sent")
	}
}

func TestGoogleIdentityUnavailable(t *testing.T) {
	fakeIdentity(t, nil, errors.New("could not find default credentials"))
	_, err := New(connector.Config{"endpoint_url": "https://m.run.app/mcp", "transport": "streamable", "google_identity": true},
		connector.Env{HTTPClient: http.DefaultClient})
	if err == nil || !strings.Contains(err.Error(), "no Google identity") {
		t.Fatalf("an unusable identity must be explained at build time: %v", err)
	}
	// stdio servers have no URL to sign for: the option is ignored there.
	if _, err := New(connector.Config{"transport": "stdio", "command": "true", "google_identity": true},
		connector.Env{HTTPClient: http.DefaultClient}); err != nil {
		t.Fatalf("stdio must ignore google_identity: %v", err)
	}
}

// GitHub's MCP server asks the client to mirror annotated parameters (x-mcp-header) into
// Mcp-Param-* headers and answers 400 without them. The SDK only knows which ones from a
// tools/list on the same session, so a call on a fresh session must list first.
func TestCallToolSendsMirroredParamHeaders(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "gh", Version: "test"}, nil)
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"owner": map[string]any{"type": "string", "x-mcp-header": "Owner"}}}
	srv.AddTool(&mcp.Tool{Name: "issue_read", InputSchema: schema},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "read"}}}, nil
		})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(ts.Close)

	inst, err := New(connector.Config{"endpoint_url": ts.URL, "transport": "streamable_http"}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	text, isErr, err := inst.(*Server).CallTool(context.Background(), "issue_read", []byte(`{"owner":"j75689"}`))
	if err != nil || isErr || text != "read" {
		t.Fatalf("call: %q %v %v", text, isErr, err)
	}
}

// A server behind Cloudflare Access needs the service token's headers on every request, over either
// HTTP transport; they are kept with the secret fields, not in the plain-text headers.
func TestSecretHeadersReachTheServer(t *testing.T) {
	streamable := func(g func(*http.Request) *mcp.Server) http.Handler {
		return mcp.NewStreamableHTTPHandler(g, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	}
	for _, tc := range []struct {
		name, transport string
		identity        bool
		handler         func(func(*http.Request) *mcp.Server) http.Handler
	}{
		{"streamable_http", "streamable_http", false, streamable},
		{"sse", "sse", false, func(g func(*http.Request) *mcp.Server) http.Handler { return mcp.NewSSEHandler(g, nil) }},
		// google_identity swaps the HTTP client; the headers are added around it.
		{"google_identity", "streamable_http", true, streamable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := mcp.NewServer(&mcp.Implementation{Name: "cf", Version: "test"}, nil)
			srv.AddTool(&mcp.Tool{Name: "ping", InputSchema: map[string]any{"type": "object"}},
				func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil
				})
			mcpHandler := tc.handler(func(*http.Request) *mcp.Server { return srv })
			var mu sync.Mutex
			requests, missing := 0, 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests++
				if r.Header.Get("CF-Access-Client-Id") != "cid" || r.Header.Get("CF-Access-Client-Secret") != "csecret" ||
					tc.identity && r.Header.Get(googleid.Header) != "Bearer tok-9" {
					missing++
				}
				mu.Unlock()
				mcpHandler.ServeHTTP(w, r)
			}))
			t.Cleanup(ts.Close)

			fakeIdentity(t, staticToken("tok-9"), nil)
			inst, err := New(connector.Config{"endpoint_url": ts.URL, "transport": tc.transport, "google_identity": tc.identity,
				"secret_headers": "# service token\nCF-Access-Client-Id: cid\n\nCF-Access-Client-Secret: csecret\n"}, connector.Env{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			text, isErr, err := inst.(*Server).CallTool(ctx, "ping", []byte(`{}`))
			if err != nil || isErr || text != "pong" {
				t.Fatalf("call: %q %v %v", text, isErr, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if requests == 0 || missing != 0 {
				t.Fatalf("%d of %d requests lacked the access headers", missing, requests)
			}
		})
	}
}

// Same order as mcp_action: plain headers, then secret headers, then the bearer token in Authorization.
func TestSecretHeadersPrecedence(t *testing.T) {
	inst, err := New(connector.Config{"endpoint_url": "https://mcp.example.com/mcp", "transport": "streamable_http",
		"headers":        map[string]any{"X-Env": "plain", "Authorization": "plain", "X-Plain": "kept"},
		"secret_headers": "X-Env: secret\nAuthorization: secret",
		"auth_token":     "tok"}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	h := inst.(*Server).opts.Headers
	if h["X-Env"] != "secret" || h["Authorization"] != "Bearer tok" || h["X-Plain"] != "kept" {
		t.Fatalf("headers: %v", h)
	}
	inst, err = New(connector.Config{"endpoint_url": "https://mcp.example.com/mcp", "transport": "streamable_http",
		"secret_headers": "Authorization: Basic abc"}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if got := inst.(*Server).opts.Headers["Authorization"]; got != "Basic abc" {
		t.Fatalf("without a bearer token the secret Authorization stays: %q", got)
	}
}

// Header names are case-insensitive on the wire, so the precedence must hold whatever case each
// field uses; otherwise every request picks a random winner among the spellings.
func TestSecretHeadersPrecedenceOnTheWire(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "case", Version: "test"}, nil)
	srv.AddTool(&mcp.Tool{Name: "ping", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil
		})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	want := map[string]string{"X-Env": "secret", "X-Over": "secret", "Authorization": "Bearer tok"}
	var mu sync.Mutex
	requests, wrong := 0, map[string]int{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		for name, v := range want {
			if got := r.Header.Values(name); len(got) != 1 || got[0] != v {
				wrong[name+"="+strings.Join(got, ",")]++
			}
		}
		mu.Unlock()
		mcpHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	inst, err := New(connector.Config{"endpoint_url": ts.URL, "transport": "streamable_http",
		"headers":        map[string]any{"x-env": "plain", "X-Over": "plain"},
		"secret_headers": "X-Env: secret\nx-over: secret\nauthorization: Basic abc",
		"auth_token":     "tok"}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Each call makes several requests, and each request used to merge in a new random order.
	for range 10 {
		if text, isErr, err := inst.(*Server).CallTool(ctx, "ping", []byte(`{}`)); err != nil || isErr || text != "pong" {
			t.Fatalf("call: %q %v %v", text, isErr, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if requests == 0 || len(wrong) != 0 {
		t.Fatalf("over %d requests, wrong header values (name=values: count): %v", requests, wrong)
	}
}

// secret_env values are secrets too: a malformed line is named by its number, never quoted.
func TestSecretEnvMalformed(t *testing.T) {
	_, err := New(connector.Config{"transport": "stdio", "command": "true",
		"secret_env": "# tokens\nAPI_KEY=k\n\nsupersecretvalue"}, connector.Env{})
	if err == nil || !strings.Contains(err.Error(), "secret environment") || !strings.Contains(err.Error(), "line 4") {
		t.Fatalf("want a secret environment error naming the line: %v", err)
	}
	if strings.Contains(err.Error(), "supersecretvalue") {
		t.Fatalf("the error must not quote the secret: %v", err)
	}
}

func TestSecretHeadersMalformed(t *testing.T) {
	_, err := New(connector.Config{"endpoint_url": "https://mcp.example.com/mcp", "transport": "streamable_http",
		"secret_headers": "CF-Access-Client-Id: cid\nsupersecretvalue"}, connector.Env{})
	if err == nil || !strings.Contains(err.Error(), "secret headers") || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("want a secret headers error naming the line: %v", err)
	}
	if strings.Contains(err.Error(), "supersecretvalue") {
		t.Fatalf("the error must not quote the secret: %v", err)
	}
	// Existing configs without the field, and stdio servers, are unaffected.
	if _, err := New(connector.Config{"transport": "stdio", "command": "true"}, connector.Env{}); err != nil {
		t.Fatal(err)
	}
}

// A bad header fails at New, so Test connection and agent tool loading show it instead of every
// request. A secret line is named by its number only: a pasted secret may sit on either side of
// the colon. The number counts every line the user sees: leading blank lines, comments, CRLF.
func TestSecretHeadersInvalid(t *testing.T) {
	prefixes := map[string]int{
		"# token\nX-Ok: fine\n": 3,
		"\n\n":                  3,
		"  \r\n# c\r\n  # indented\r\nX-Ok: fine\r\n": 5,
		"  # token\r\n \t \r\nX-Ok: fine\r\n":         4,
	}
	for _, line := range []string{
		"X-Empty:",
		"X-Empty:   ",
		"Bad Name: s3cret",
		"Bad(Name): s3cret",
		"X-Ctl: s3\rcret",
		"X-Ctl: s3\x00cret",
		"X-Ctl: s3\x7fcret",
	} {
		for prefix, n := range prefixes {
			_, err := New(connector.Config{"endpoint_url": "https://mcp.example.com/mcp", "transport": "streamable_http",
				"secret_headers": prefix + line}, connector.Env{})
			want := fmt.Sprintf("mcp server: secret headers: line %d: ", n)
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("%q after %q: want a secret headers error naming line %d: %v", line, prefix, n, err)
				continue
			}
			name, value, _ := strings.Cut(line, ":")
			for _, part := range []string{name, strings.TrimSpace(value)} {
				if part != "" && strings.Contains(err.Error(), part) {
					t.Errorf("%q: the error quotes %q: %v", line, part, err)
				}
			}
		}
	}
}

// The UI always sends a string, but an API client may not: a non-string fails like a bad line
// rather than leaving the server without its secret headers.
func TestSecretHeadersNotAString(t *testing.T) {
	for _, v := range []any{123, true} {
		_, err := New(connector.Config{"endpoint_url": "https://mcp.example.com/mcp", "transport": "streamable_http",
			"secret_headers": v}, connector.Env{})
		if err == nil || !strings.HasPrefix(err.Error(), "mcp server: secret headers: line 1: ") {
			t.Errorf("%v: want a secret headers error naming line 1: %v", v, err)
			continue
		}
		if strings.Contains(err.Error(), fmt.Sprint(v)) {
			t.Errorf("%v: the error quotes the value: %v", v, err)
		}
	}
	// stdio does not read the field at all.
	if _, err := New(connector.Config{"transport": "stdio", "command": "true", "secret_headers": 123}, connector.Env{}); err != nil {
		t.Fatal(err)
	}
}

func TestPlainHeadersInvalid(t *testing.T) {
	for name, value := range map[string]string{"Bad Name": "s3cret", "X-Ctl": "s3cret\r\nX-Injected: 1", "": "s3cret"} {
		_, err := New(connector.Config{"endpoint_url": "https://mcp.example.com/mcp", "transport": "streamable_http",
			"headers": map[string]any{name: value}}, connector.Env{})
		if err == nil || !strings.HasPrefix(err.Error(), "mcp server: header ") {
			t.Errorf("%q: want a header error: %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%q: the error quotes the value: %v", name, err)
		}
	}
	// An empty plain value is still sent as it is.
	inst, err := New(connector.Config{"endpoint_url": "https://mcp.example.com/mcp", "transport": "streamable_http",
		"headers": map[string]any{"X-Empty": ""}}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := inst.(*Server).opts.Headers["X-Empty"]; !ok || v != "" {
		t.Fatalf("headers: %v", inst.(*Server).opts.Headers)
	}
}

// stdio sends no headers, so a header field it cannot use does not stop it, and neither the
// header fields nor the bearer token end up in its headers.
func TestStdioIgnoresHeaders(t *testing.T) {
	inst, err := New(connector.Config{"transport": "stdio", "command": "true",
		"headers":        map[string]any{"Bad Name": "v"},
		"secret_headers": "no colon\nX-Empty:",
		"auth_token":     "tok"}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if h := inst.(*Server).opts.Headers; len(h) != 0 {
		t.Fatalf("headers: %v", h)
	}
}
