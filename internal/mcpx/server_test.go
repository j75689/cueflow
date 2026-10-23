package mcpx

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNoStandaloneStream(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "ping"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, struct{}{}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(NoStandaloneStream(h))
	defer ts.Close()

	// GET is refused at once, not held open
	start := time.Now()
	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	req.Header.Set("Accept", "text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed || time.Since(start) > 2*time.Second {
		t.Fatalf("GET: %d after %s", res.StatusCode, time.Since(start))
	}

	// an MCP client still works: it connects, lists and calls tools over POST
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()
	out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ping"})
	if err != nil || out.Content[0].(*mcp.TextContent).Text != "pong" {
		t.Fatalf("call: %v %v", out, err)
	}
}

// A client on protocol 2026-07-28 opens subscriptions/listen when the server advertises
// tools/prompts/resources.listChanged, and the SDK holds that POST open until the client goes away: on
// Cloud Run each one took a request slot for 30 minutes. With StatelessCapabilities the client opens none.
func TestNoPOSTIsHeldOpen(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, &mcp.ServerOptions{Capabilities: StatelessCapabilities()})
	mcp.AddTool(srv, &mcp.Tool{Name: "ping"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, struct{}{}, nil
	})
	srv.AddPrompt(&mcp.Prompt{Name: "hello"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{}, nil
	})
	srv.AddResource(&mcp.Resource{URI: "cueflow://t", Name: "t"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{}, nil
	})
	h := Stateless(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}), time.Minute)
	var mu sync.Mutex
	var slow []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		if d := time.Since(start); d > time.Second {
			mu.Lock()
			slow = append(slow, fmt.Sprintf("%s %s after %s", r.Method, r.Header.Get("Mcp-Method"), d))
			mu.Unlock()
		}
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler:     func(context.Context, *mcp.ToolListChangedRequest) {}, // what makes a client listen
		PromptListChangedHandler:   func(context.Context, *mcp.PromptListChangedRequest) {},
		ResourceListChangedHandler: func(context.Context, *mcp.ResourceListChangedRequest) {},
	})
	cs, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ping"}); err != nil || out.Content[0].(*mcp.TextContent).Text != "pong" {
		t.Fatalf("call: %v %v", out, err)
	}
	time.Sleep(1500 * time.Millisecond) // a POST held open would now be past the mark
	cs.Close()
	ts.CloseClientConnections()
	mu.Lock()
	defer mu.Unlock()
	if len(slow) > 0 {
		t.Fatalf("POSTs held open: %v", slow)
	}
}

func TestStatelessCapabilitiesPinListChangedOff(t *testing.T) {
	c := StatelessCapabilities()
	if c.Tools == nil || c.Tools.ListChanged || c.Prompts == nil || c.Prompts.ListChanged ||
		c.Resources == nil || c.Resources.ListChanged || c.Resources.Subscribe {
		t.Fatalf("tools %+v prompts %+v resources %+v", c.Tools, c.Prompts, c.Resources)
	}
}

func TestRequestDeadline(t *testing.T) {
	h := withDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // a handler that waits for the client
	}), 100*time.Millisecond)
	ts := httptest.NewServer(h)
	defer ts.Close()
	start := time.Now()
	res, err := http.Post(ts.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("request ended after %s", d)
	}
}

// The SDK runs a tool on a ctx detached from its request (as configured, without PropagateRequestCancellation):
// the tool reads its request's deadline with RequestDeadline, and its ctx has none, so nothing cuts it.
func TestDeadlineIsReadableInTheTool(t *testing.T) {
	type seen struct {
		at, ctxDeadline time.Time
		ok, hasDeadline bool
	}
	got := make(chan seen, 1)
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, &mcp.ServerOptions{Capabilities: StatelessCapabilities()})
	mcp.AddTool(srv, &mcp.Tool{Name: "read"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct{}, error) {
		var s seen
		s.at, s.ok = RequestDeadline(ctx)
		s.ctxDeadline, s.hasDeadline = ctx.Deadline()
		got <- s
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "read"}}}, struct{}{}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(Stateless(h, 300*time.Millisecond))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()
	start := time.Now()
	_, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "read"})
	end := time.Now()
	var s seen
	select {
	case s = <-got:
	case <-ctx.Done():
		t.Fatal("the tool was not called")
	}
	if d := 300 * time.Millisecond; !s.ok || !s.at.After(start.Add(d)) || s.at.After(end.Add(d)) {
		t.Errorf("request deadline %v %v, called from %v to %v", s.at, s.ok, start, end)
	}
	if s.hasDeadline {
		t.Errorf("the tool's ctx has a deadline (%v): it would cut the tool", s.ctxDeadline)
	}
}
