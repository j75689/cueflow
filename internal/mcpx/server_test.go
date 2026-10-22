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
// tools.listChanged, and the SDK holds that POST open until the client goes away: on Cloud Run each
// one took a request slot for 30 minutes. With StatelessCapabilities the client opens none.
func TestNoPOSTIsHeldOpen(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, &mcp.ServerOptions{Capabilities: StatelessCapabilities()})
	mcp.AddTool(srv, &mcp.Tool{Name: "ping"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, struct{}{}, nil
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
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {}, // what makes a client listen
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

// The SDK runs a tool on a ctx detached from its request; RequestContext gives it the deadline back,
// so a tool that waits on its ctx ends when the request's time is up, through the real handler.
func TestDeadlineReachesTheTool(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, &mcp.ServerOptions{Capabilities: StatelessCapabilities()})
	mcp.AddTool(srv, &mcp.Tool{Name: "wait"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct{}, error) {
		ctx, cancel := RequestContext(ctx)
		defer cancel()
		<-ctx.Done()
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ended"}}}, struct{}{}, nil
	})
	// Without PropagateRequestCancellation (or on an older protocol), the tool's ctx never ends by itself.
	for _, propagate := range []bool{true, false} {
		h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
			&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, PropagateRequestCancellation: propagate})
		callUntilDone(t, Stateless(h, 300*time.Millisecond), propagate)
	}
}

func callUntilDone(t *testing.T, h http.Handler, propagate bool) {
	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()
	start := time.Now()
	_, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "wait"}) // the answer may not make it; the request must end
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("propagate %v: the call ended after %s, not near the 300ms deadline", propagate, d)
	}
}
