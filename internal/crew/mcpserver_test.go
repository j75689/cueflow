package crew

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/mcpx"
)

// A tool call queued behind a slow one of the same run gives up when its request ends, rather than
// holding the request (and a Cloud Run slot) until the slow one finishes.
func TestQueuedToolCallGivesUpWithItsRequest(t *testing.T) {
	run := &mcpRun{done: make(chan struct{})}
	run.acquire(context.Background()) // a call in progress
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	out := run.call(ctx, nil, nil, "list_tasks", nil)
	if !out.IsError || !strings.Contains(out.Content[0].(*mcp.TextContent).Text, "request ended") || time.Since(start) > time.Second {
		t.Fatalf("got %+v after %s", out.Content[0], time.Since(start))
	}
}

// Nor does it wait for the turn as long as its request may live (CUEFLOW_MCP_REQUEST_TIMEOUT).
func TestQueuedToolCallWaitsForTheTurnOnlySoLong(t *testing.T) {
	if toolTurnWait <= externalCallTimeout {
		t.Fatalf("toolTurnWait %s: a call queued behind a normal connector call (%s) would fail", toolTurnWait, externalCallTimeout)
	}
	defer func(d time.Duration) { toolTurnWait = d }(toolTurnWait)
	toolTurnWait = 50 * time.Millisecond
	run := &mcpRun{done: make(chan struct{})}
	run.acquire(context.Background())
	start := time.Now()
	out := run.call(context.Background(), nil, nil, "list_tasks", nil)
	if !out.IsError || !strings.Contains(out.Content[0].(*mcp.TextContent).Text, "Timed out waiting") || time.Since(start) > time.Second {
		t.Fatalf("got %+v after %s", out.Content[0], time.Since(start))
	}
}

// A call that gets the turn only once its request has ended runs nothing (c is nil: running it would panic).
func TestToolCallWhoseRequestEndedIsNotRun(t *testing.T) {
	run := &mcpRun{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := run.call(ctx, nil, nil, "list_tasks", nil)
	if !out.IsError || !strings.Contains(out.Content[0].(*mcp.TextContent).Text, "was not run") {
		t.Fatalf("got %+v", out.Content[0])
	}
	if !run.acquire(context.Background()) {
		t.Fatal("the turn was not released")
	}
}

// A connector call served over MCP ends before its request does; anywhere else it keeps its own
// timeout, whatever the deadline on its ctx.
func TestExternalTimeoutFollowsTheMCPRequest(t *testing.T) {
	own := 20 * time.Minute
	short, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	long, cancelLong := context.WithTimeout(context.Background(), time.Hour)
	defer cancelLong()

	if d, by := externalTimeout(withRequestBudget(short), own); !by || d > time.Minute-requestBudgetMargin || d < 50*time.Second {
		t.Errorf("served over MCP, 1 min left: %s %v", d, by)
	}
	if d, by := externalTimeout(withRequestBudget(long), own); by || d != own {
		t.Errorf("served over MCP, more time left than the connector's own: %s %v", d, by)
	}
	if d, by := externalTimeout(withRequestBudget(context.Background()), own); by || d != own {
		t.Errorf("served over MCP, no deadline: %s %v", d, by)
	}
	if d, by := externalTimeout(short, own); by || d != own {
		t.Errorf("in-process run with a deadline: %s %v", d, by)
	}
	if d, by := externalTimeout(context.Background(), own); by || d != own {
		t.Errorf("in-process run: %s %v", d, by)
	}
}

// servedOverMCP serves one tool through the SDK's real stateless handler behind mcpx.Stateless, as
// MCPHandler does, and calls it once; it returns the tool's text (or the error) and how long it took.
// As MCPHandler configures it, the tool's ctx carries no deadline but what mcpx.RequestContext puts back.
func servedOverMCP(t *testing.T, maxRequest time.Duration, fn func(ctx context.Context) string) (string, time.Duration) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, &mcp.ServerOptions{Capabilities: mcpx.StatelessCapabilities()})
	srv.AddTool(&mcp.Tool{Name: "x", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fn(ctx)}}}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(mcpx.Stateless(h, maxRequest))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()
	start := time.Now()
	out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "x"})
	if err != nil {
		return err.Error(), time.Since(start)
	}
	return out.Content[0].(*mcp.TextContent).Text, time.Since(start)
}

// Through the real handler, a call queued behind one in progress gives up at the request's deadline.
func TestQueuedToolCallEndsWithItsRequestOverMCP(t *testing.T) {
	run := &mcpRun{done: make(chan struct{})}
	run.acquire(context.Background()) // a call in progress
	var text string
	_, d := servedOverMCP(t, 300*time.Millisecond, func(ctx context.Context) string {
		text = run.call(ctx, nil, nil, "list_tasks", nil).Content[0].(*mcp.TextContent).Text
		return text
	})
	if d > 3*time.Second || !strings.Contains(text, "request ended") {
		t.Fatalf("after %s: %q", d, text)
	}
}

// Through the real handler, a connector call sees what is left of its request.
func TestExternalTimeoutSeesTheRequestOverMCP(t *testing.T) {
	text, _ := servedOverMCP(t, time.Minute, func(ctx context.Context) string {
		ctx, cancel := mcpToolContext(ctx)
		defer cancel()
		d, by := externalTimeout(ctx, 20*time.Minute)
		return fmt.Sprintf("%v %v", by, d > 50*time.Second && d <= time.Minute-requestBudgetMargin)
	})
	if text != "true true" {
		t.Fatalf("capped, in range: %q", text)
	}
}
