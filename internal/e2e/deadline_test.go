package e2e

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/api"
	"github.com/j75689/cueflow/internal/config"
	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/mcpx"
	"github.com/j75689/cueflow/internal/store"
)

// runMCP serves the crew's run tool servers with a request deadline of maxRequest, as /mcp/run is
// served, and connects to the run of token.
func runMCP(t *testing.T, e *env, token string, maxRequest time.Duration) *mcp.ClientSession {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(crew.MCPPathPrefix, e.crew.MCPHandler(maxRequest))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cs, err := mcpx.Connect(context.Background(), mcpx.Options{Transport: mcpx.TransportStreamable,
		Endpoint: srv.URL + crew.MCPPathPrefix + token, HTTPClient: &http.Client{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// lockTable holds an exclusive lock on table (writes wait, reads don't) until the returned func is
// called, or until hold has passed, so a test that fails doesn't hang on it.
func lockTable(t *testing.T, e *env, table string, hold time.Duration) func() {
	t.Helper()
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "LOCK TABLE "+table+" IN EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(hold, func() { _ = tx.Rollback(ctx) })
	release := func() { timer.Stop(); _ = tx.Rollback(ctx) }
	t.Cleanup(release)
	return release
}

// callAsync calls a tool in the background; the result is its text, or the transport's error.
func callAsync(cs *mcp.ClientSession, name string, args map[string]any) <-chan string {
	out := make(chan string, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		switch {
		case err != nil:
			out <- "error: " + err.Error()
		case len(res.Content) == 0:
			out <- ""
		default:
			out <- res.Content[0].(*mcp.TextContent).Text
		}
	}()
	return out
}

// A built-in tool served over MCP is not cut by its request's deadline: one held up past it (here by a
// locked table) still makes all its writes, rather than stopping halfway through them. Its answer,
// written after the deadline, does not reach the client, so the test does not look at it.
func TestBuiltInToolOutlivesItsMCPRequest(t *testing.T) {
	t.Run("agent token, claim_task", func(t *testing.T) {
		e := setup(t)
		ctx := context.Background()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		srv := httptest.NewServer((&api.Server{Store: e.store, Hub: hub.New(e.store, logger, config.DefaultConnectorHTTPTimeout), Broker: events.NewBroker(),
			Scheduler: e.sched, Crew: e.crew, Logger: logger, MCPToken: testMCPToken, MCPRequestTimeout: 500 * time.Millisecond}).Handler())
		defer srv.Close()
		var rt store.Connector
		e.do("POST", "/connectors", map[string]any{"name": "Self-driven", "type": "external", "config": map[string]any{}}, &rt)
		var a store.Agent
		e.do("POST", "/agents", map[string]any{"name": "Outside writer", "slug": "outside", "capabilities": []string{"post_deliverables"},
			"runtime_connector_id": rt.ID}, &a)
		var tok struct {
			Token string `json:"token"`
		}
		e.do("POST", "/agents/"+a.ID.String()+"/tokens", map[string]any{"name": "laptop"}, &tok)
		var task store.Task
		e.do("POST", "/tasks", map[string]any{"title": "Slow claim", "assignee_agent_id": a.ID}, &task)
		cs, err := mcpx.Connect(ctx, mcpx.Options{Transport: mcpx.TransportStreamable, Endpoint: srv.URL + "/mcp",
			HTTPClient: &http.Client{Transport: bearer{tok.Token}}})
		if err != nil {
			t.Fatal(err)
		}
		defer cs.Close()

		release := lockTable(t, e, "agent_runs", 5*time.Second)
		reply := callAsync(cs, "claim_task", map[string]any{})
		time.Sleep(1500 * time.Millisecond) // well past the deadline
		release()
		select {
		case <-reply:
		case <-time.After(10 * time.Second):
			t.Fatal("claim_task did not end")
		}
		got, _ := e.store.GetTask(ctx, task.ID)
		_, runErr := e.store.RunningAgentRun(ctx, task.ID, a.ID)
		if thread := threadText(t, e, task.ID.String()); got.Status != store.TaskInProgress || runErr != nil || !strings.Contains(thread, "claimed the task") {
			t.Fatalf("claimed halfway: status %s, run %v, thread:\n%s", got.Status, runErr, thread)
		}
	})

	t.Run("run token, create_subtask", func(t *testing.T) {
		e, team := crewSetup(t)
		ctx := context.Background()
		var task store.Task
		e.do("POST", "/tasks", map[string]any{"title": "Remote", "assignee_agent_id": team["chief"].ID}, &task)
		cl := claim(t, e, task)
		token, err := e.store.CreateRunToken(ctx, task.ID, team["chief"].ID, &cl.RunID, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		cs := runMCP(t, e, token, 500*time.Millisecond)

		release := lockTable(t, e, "tasks", 5*time.Second)
		reply := callAsync(cs, "create_subtask", map[string]any{"title": "Draft", "description": "Write the draft.", "assignee": "writer"})
		time.Sleep(1500 * time.Millisecond)
		release()
		select {
		case <-reply:
		case <-time.After(10 * time.Second):
			t.Fatal("create_subtask did not end")
		}
		var subs int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE parent_id=$1`, task.ID).Scan(&subs); err != nil {
			t.Fatal(err)
		}
		if thread := threadText(t, e, task.ID.String()); subs != 1 || !strings.Contains(thread, "tool_result:Subtask #") {
			t.Fatalf("%d subtasks, thread:\n%s", subs, thread)
		}
	})
}

// A connector call served over MCP is still capped by what is left of its request (less 5 s for the
// answer), and the agent gets that answer before the request ends.
func TestConnectorCallIsCappedByItsMCPRequest(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	tools := mcp.NewServer(&mcp.Implementation{Name: "slow", Version: "test"}, nil)
	tools.AddTool(&mcp.Tool{Name: "slow_render", InputSchema: map[string]any{"type": "object"}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			select {
			case <-time.After(4 * time.Second):
			case <-ctx.Done():
			case <-stop:
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "rendered"}}}, nil
		})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return tools },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(ts.Close)
	var conn store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "Slow", "type": "mcp_server",
		"config": map[string]any{"endpoint_url": ts.URL}}, &conn); code != 201 {
		t.Fatalf("create connector: %d", code)
	}
	chief := team["chief"]
	if code := e.do("PATCH", "/agents/"+chief.ID.String(), map[string]any{"tool_connector_ids": []string{conn.ID.String()}}, nil); code != 200 {
		t.Fatalf("attach server: %d", code)
	}
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Render", "assignee_agent_id": chief.ID}, &task)
	cl := claim(t, e, task)
	token, err := e.store.CreateRunToken(ctx, task.ID, chief.ID, &cl.RunID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cs := runMCP(t, e, token, 6*time.Second) // 1 s for the call past the 5 s margin

	start := time.Now()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "slow__slow_render", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("no answer after %s: %v", time.Since(start), err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if d := time.Since(start); !res.IsError || !strings.Contains(text, "CUEFLOW_MCP_REQUEST_TIMEOUT") || d > 3*time.Second {
		t.Fatalf("after %s: %v %q", d, res.IsError, text)
	}
	if thread := threadText(t, e, task.ID.String()); !strings.Contains(thread, "tool_result:error: the call to slow_render was stopped") {
		t.Fatalf("thread:\n%s", thread)
	}
}
