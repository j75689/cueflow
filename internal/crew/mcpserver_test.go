package crew

import (
	"context"
	"testing"
	"time"
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
	if !out.IsError || time.Since(start) > time.Second {
		t.Fatalf("got %+v after %s", out, time.Since(start))
	}
}

// Nor does it wait for the turn as long as its request may live (CUEFLOW_MCP_REQUEST_TIMEOUT).
func TestQueuedToolCallWaitsForTheTurnOnlySoLong(t *testing.T) {
	defer func(d time.Duration) { toolTurnWait = d }(toolTurnWait)
	toolTurnWait = 50 * time.Millisecond
	run := &mcpRun{done: make(chan struct{})}
	run.acquire(context.Background())
	start := time.Now()
	out := run.call(context.Background(), nil, nil, "list_tasks", nil)
	if !out.IsError || time.Since(start) > time.Second {
		t.Fatalf("got %+v after %s", out, time.Since(start))
	}
}
