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

// A connector call served over MCP (one with an mcpx.RequestDeadline) ends before its request does;
// anywhere else it keeps its own timeout, whatever the deadline on its ctx.
func TestExternalTimeoutFollowsTheMCPRequest(t *testing.T) {
	own := 20 * time.Minute
	bg := context.Background()
	inProcess, cancel := context.WithTimeout(bg, time.Minute)
	defer cancel()

	if d, by := externalTimeout(mcpx.WithRequestDeadline(bg, time.Now().Add(time.Minute)), own); !by || d > time.Minute-requestBudgetMargin || d < 50*time.Second {
		t.Errorf("served over MCP, 1 min left: %s %v", d, by)
	}
	if d, by := externalTimeout(mcpx.WithRequestDeadline(bg, time.Now().Add(time.Hour)), own); by || d != own {
		t.Errorf("served over MCP, more time left than the connector's own: %s %v", d, by)
	}
	if d, by := externalTimeout(mcpx.WithRequestDeadline(bg, time.Now().Add(-time.Second)), own); !by || d > 0 {
		t.Errorf("served over MCP, deadline passed: %s %v", d, by)
	}
	if d, by := externalTimeout(inProcess, own); by || d != own {
		t.Errorf("in-process run with a deadline: %s %v", d, by)
	}
	if d, by := externalTimeout(bg, own); by || d != own {
		t.Errorf("in-process run: %s %v", d, by)
	}
}

// servedOverMCP serves one tool through the SDK's real stateless handler behind mcpx.Stateless, as
// MCPHandler does, and calls it once; it returns the tool's text (or the error) and how long it took.
// As MCPHandler configures it, the tool's ctx has no deadline, only the request's mcpx.RequestDeadline.
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

// Through the real handler, a call queued behind one in progress gives up requestBudgetMargin before
// its request's deadline, so the agent gets the answer: one written after the deadline is lost.
func TestQueuedToolCallAnswersBeforeItsRequestEnds(t *testing.T) {
	run := &mcpRun{done: make(chan struct{})}
	run.acquire(context.Background()) // a call in progress
	text, d := servedOverMCP(t, requestBudgetMargin+300*time.Millisecond, func(ctx context.Context) string {
		return run.call(ctx, nil, nil, "list_tasks", nil).Content[0].(*mcp.TextContent).Text
	})
	if d > 2*time.Second || !strings.Contains(text, "Timed out waiting") {
		t.Fatalf("after %s: %q", d, text)
	}
}

// A free turn is taken even with less than requestBudgetMargin left of the request, when the wait for
// one has already run out (the run has finished, so the call answers that without a Crew).
func TestFreeTurnIsTakenWithItsRequestEnding(t *testing.T) {
	run := &mcpRun{done: make(chan struct{})}
	close(run.done)
	for i := 0; i < 20; i++ { // a select between the turn and the ended wait would pick at random
		ctx := mcpx.WithRequestDeadline(context.Background(), time.Now().Add(time.Second))
		out := run.call(ctx, nil, nil, "list_tasks", nil)
		if text := out.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "already finished") {
			t.Fatalf("call %d: %q", i, text)
		}
	}
}

// Through the real handler, a connector call sees what is left of its request, and the tool itself has
// no deadline: it runs to the end.
func TestExternalTimeoutSeesTheRequestOverMCP(t *testing.T) {
	text, _ := servedOverMCP(t, time.Minute, func(ctx context.Context) string {
		d, by := externalTimeout(ctx, 20*time.Minute)
		_, has := ctx.Deadline()
		return fmt.Sprintf("%v %v %v", by, d > 50*time.Second && d <= time.Minute-requestBudgetMargin, has)
	})
	if text != "true true false" {
		t.Fatalf("capped, in range, tool ctx with a deadline: %q", text)
	}
}

// A Crew not built with New (as in tests) has no DetachedWriteTimeout: detached falls back to the
// default rather than failing every thread write at once. Its ctx outlives the caller's.
func TestDetachedFallsBackToTheDefaultTimeout(t *testing.T) {
	for _, set := range []time.Duration{0, -time.Second} {
		parent, stop := context.WithCancel(context.Background())
		stop()
		ctx, cancel := (&Crew{DetachedWriteTimeout: set}).detached(parent)
		at, ok := ctx.Deadline()
		left, err := time.Until(at), ctx.Err()
		cancel()
		if !ok || left <= DefaultDetachedWriteTimeout-time.Second || left > DefaultDetachedWriteTimeout {
			t.Fatalf("DetachedWriteTimeout %v: deadline in %v (%v), want about %v", set, left, ok, DefaultDetachedWriteTimeout)
		}
		if err != nil {
			t.Fatalf("DetachedWriteTimeout %v: ctx ended with its parent", set)
		}
	}
}
