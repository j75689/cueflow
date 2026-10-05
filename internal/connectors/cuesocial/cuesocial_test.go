package cuesocial

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/connector"
)

// fakeCueSocial mimics CueSocial's MCP tools. createStatus decides how create_post ends.
type fakeCueSocial struct {
	mu           sync.Mutex
	createStatus string
	block        chan struct{}
	calls        []string
	lastCreate   map[string]any
	headers      http.Header
}

func (f *fakeCueSocial) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

func jsonResult(v any) *mcp.CallToolResult {
	b, _ := json.Marshal(v)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

func (f *fakeCueSocial) server(t *testing.T) *httptest.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "cuesocial", Version: "test"}, nil)
	srv.AddTool(&mcp.Tool{Name: "list_accounts", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			f.record("list_accounts")
			return jsonResult(map[string]any{"accounts": []map[string]any{
				{"id": "th1", "platform": "threads", "username": "brand"},
				{"id": "th2", "platform": "threads", "username": "me"},
				{"id": "ig1", "platform": "instagram", "username": "brand.ig"},
			}}), nil
		})
	srv.AddTool(&mcp.Tool{Name: "create_post", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			f.record("create_post")
			var in map[string]any
			_ = json.Unmarshal(req.Params.Arguments, &in)
			f.mu.Lock()
			f.lastCreate = in
			status, block := f.createStatus, f.block
			f.mu.Unlock()
			if block != nil {
				select {
				case <-block:
				case <-ctx.Done():
				}
			}
			if status == "rejected" {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "Instagram needs an image"}}}, nil
			}
			results := []map[string]any{{"account": "th1", "label": "threads @brand", "published": true, "permalink": "https://threads.net/p/1"}}
			if status == "partial" {
				results = append(results, map[string]any{"account": "th2", "label": "threads @me", "published": false, "error": "token expired"})
			}
			if status == "scheduled" {
				results = nil
			}
			return jsonResult(map[string]any{"id": "post-1", "status": status, "results": results}), nil
		})
	srv.AddTool(&mcp.Tool{Name: "publish_post", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			f.record("publish_post")
			return jsonResult(map[string]any{"id": "post-1", "status": "published", "results": []map[string]any{
				{"account": "th1", "label": "threads @brand", "published": true, "permalink": "https://threads.net/p/1"},
				{"account": "th2", "label": "threads @me", "published": true, "permalink": "https://threads.net/p/2"},
			}}), nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.headers = r.Header.Clone()
		f.mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func newPublisher(t *testing.T, f *fakeCueSocial) *Publisher {
	ts := f.server(t)
	inst, err := New(connector.Config{"endpoint_url": ts.URL, "cf_client_id": "cid", "cf_client_secret": "csecret"},
		connector.Env{HTTPClient: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return inst.(*Publisher)
}

func req(accounts ...string) connector.PublishRequest {
	return connector.PublishRequest{PostID: uuid.New(), Platforms: []string{"threads"}, Parts: []string{"Hello"},
		Hashtags: []string{"mcp"}, Accounts: accounts}
}

func TestTestAndAccounts(t *testing.T) {
	f := &fakeCueSocial{createStatus: "published"}
	p := newPublisher(t, f)
	res, _ := p.Test(context.Background())
	if !res.OK || !strings.Contains(res.Message, "3 accounts") || !strings.Contains(res.Message, "threads @brand") {
		t.Fatalf("test: %+v", res)
	}
	if f.headers.Get("CF-Access-Client-Id") != "cid" || f.headers.Get("CF-Access-Client-Secret") != "csecret" {
		t.Fatalf("access headers not sent: %v", f.headers)
	}
	accts, err := p.ListAccounts(context.Background())
	if err != nil || len(accts) != 3 || accts[2] != (connector.Account{ID: "ig1", Platform: "instagram", Name: "@brand.ig"}) {
		t.Fatalf("accounts: %+v %v", accts, err)
	}
}

func TestPublish(t *testing.T) {
	f := &fakeCueSocial{createStatus: "published"}
	p := newPublisher(t, f)
	res, err := p.Publish(context.Background(), req("th1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalIDs[PostIDKey] != "post-1" || res.ExternalIDs["threads @brand"] != "https://threads.net/p/1" {
		t.Fatalf("ids: %v", res.ExternalIDs)
	}
	if f.lastCreate["text"] != "Hello\n\n#mcp" || f.lastCreate["scheduled_at"] != nil {
		t.Fatalf("create args: %v", f.lastCreate)
	}
}

func TestPublishValidation(t *testing.T) {
	f := &fakeCueSocial{createStatus: "published"}
	p := newPublisher(t, f)
	cases := map[string]connector.PublishRequest{
		"no CueSocial accounts chosen": req(),
		"no longer connected":          req("gone"),
		"not one of this post's":       req("ig1"),
	}
	long := req("th1")
	long.Parts = []string{strings.Repeat("字", 501)}
	cases["allows 500 characters"] = long
	img := req("th1")
	img.MediaURLs = []string{"http://10.0.0.1/a.png"}
	cases["public https URL"] = img
	for want, r := range cases {
		if _, err := p.Publish(context.Background(), r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
	for _, c := range f.calls {
		if c == "create_post" {
			t.Fatal("invalid posts must not reach create_post")
		}
	}
}

func TestScheduled(t *testing.T) {
	f := &fakeCueSocial{createStatus: "scheduled"}
	p := newPublisher(t, f)
	r := req("th1", "th2")
	at := time.Now().Add(time.Hour)
	r.ScheduledAt = &at
	r.MediaURLs = []string{"https://cdn.example.com/a.png"}
	res, err := p.Publish(context.Background(), r)
	if err != nil || res.Details["cuesocial_status"] != "scheduled" {
		t.Fatalf("scheduled: %+v %v", res, err)
	}
	if f.lastCreate["scheduled_at"] != at.UTC().Format(time.RFC3339) || f.lastCreate["image_url"] != "https://cdn.example.com/a.png" {
		t.Fatalf("create args: %v", f.lastCreate)
	}
}

func TestPartialThenRetryUsesPublishPost(t *testing.T) {
	f := &fakeCueSocial{createStatus: "partial"}
	p := newPublisher(t, f)
	_, err := p.Publish(context.Background(), req("th1", "th2"))
	var partial *connector.PartialError
	if !errors.As(err, &partial) || partial.ExternalIDs[PostIDKey] != "post-1" || !strings.Contains(err.Error(), "token expired") {
		t.Fatalf("partial: %v", err)
	}
	r := req("th1", "th2")
	r.Previous = partial.ExternalIDs
	res, err := p.Publish(context.Background(), r)
	if err != nil || res.ExternalIDs["threads @me"] != "https://threads.net/p/2" {
		t.Fatalf("retry: %+v %v", res, err)
	}
	creates := 0
	for _, c := range f.calls {
		if c == "create_post" {
			creates++
		}
	}
	if creates != 1 || f.calls[len(f.calls)-1] != "publish_post" {
		t.Fatalf("retry must call publish_post, not create_post again: %v", f.calls)
	}
}

func TestRejectedIsPlainError(t *testing.T) {
	f := &fakeCueSocial{createStatus: "rejected"}
	p := newPublisher(t, f)
	_, err := p.Publish(context.Background(), req("th1"))
	var partial *connector.PartialError
	if err == nil || errors.As(err, &partial) || !strings.Contains(err.Error(), "needs an image") {
		t.Fatalf("rejected: %v", err)
	}
}

func TestTimeoutWarnsItMayHaveGoneOut(t *testing.T) {
	f := &fakeCueSocial{createStatus: "published", block: make(chan struct{})}
	defer close(f.block)
	p := newPublisher(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := p.Publish(ctx, req("th1"))
	var partial *connector.PartialError
	if err == nil || errors.As(err, &partial) || !strings.Contains(err.Error(), "may have been published") {
		t.Fatalf("timeout: %v", err)
	}
}
