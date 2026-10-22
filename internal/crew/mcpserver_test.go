package crew

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
