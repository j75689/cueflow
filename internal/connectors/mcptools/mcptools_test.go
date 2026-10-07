package mcptools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
