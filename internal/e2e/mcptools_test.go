package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/jobs"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

// notesServer is a fake external MCP server with one read-only tool, one plain tool, one
// destructive tool and one the operator will deny.
func notesServer(t *testing.T, created *atomic.Int32) *httptest.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "notes", Version: "test"}, nil)
	ro, destructive := true, true
	text := func(s string) *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
	}
	obj := map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}}
	srv.AddTool(&mcp.Tool{Name: "search_notes", InputSchema: obj, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: ro}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return text("note: launch is Friday"), nil
		})
	srv.AddTool(&mcp.Tool{Name: "create_page", InputSchema: obj},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			created.Add(1)
			return text("page created: https://notes.example/p/1"), nil
		})
	srv.AddTool(&mcp.Tool{Name: "wipe_workspace", InputSchema: obj, Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return text("wiped"), nil })
	srv.AddTool(&mcp.Tool{Name: "secret_tool", InputSchema: obj},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return text("secret"), nil })
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestMCPToolPermissionsAndApproval(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	var created atomic.Int32
	ts := notesServer(t, &created)

	var conn store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "Notes", "type": "mcp_server",
		"config": map[string]any{"endpoint_url": ts.URL}}, &conn); code != 201 {
		t.Fatalf("create connector: %d", code)
	}
	var perms struct {
		Tools []struct {
			Name, Default, Permission string
			Risky                     bool
		} `json:"tools"`
	}
	if code := e.do("GET", "/connectors/"+conn.ID.String()+"/tool-permissions", nil, &perms); code != 200 || len(perms.Tools) != 4 {
		t.Fatalf("permissions: %d %+v", code, perms)
	}
	want := map[string]string{"search_notes": "allow", "create_page": "ask", "wipe_workspace": "ask", "secret_tool": "ask"}
	for _, tl := range perms.Tools {
		if tl.Default != want[tl.Name] || (tl.Name == "wipe_workspace") != tl.Risky {
			t.Errorf("%s: default %s risky %v", tl.Name, tl.Default, tl.Risky)
		}
	}
	if code := e.do("PUT", "/connectors/"+conn.ID.String()+"/tool-permissions",
		map[string]any{"permissions": map[string]string{"secret_tool": "deny", "x": "maybe"}}, nil); code != 400 {
		t.Fatalf("bad permission value accepted: %d", code)
	}
	e.do("PUT", "/connectors/"+conn.ID.String()+"/tool-permissions", map[string]any{"permissions": map[string]string{"secret_tool": "deny"}}, nil)

	researcher := team["researcher"]
	if code := e.do("PATCH", "/agents/"+researcher.ID.String(), map[string]any{"tool_connector_ids": []string{conn.ID.String()}}, nil); code != 200 {
		t.Fatalf("attach server: %d", code)
	}
	if code := e.do("PATCH", "/agents/"+researcher.ID.String(), map[string]any{"tool_connector_ids": []string{team["writer"].ID.String()}}, nil); code != 400 {
		t.Fatalf("non-tool connector accepted: %d", code)
	}

	var seenTools []string
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		seenTools = nil
		for _, tl := range req.Tools {
			seenTools = append(seenTools, tl.Name)
		}
		res := toolResults(req)
		b := brief(req)
		switch {
		case strings.Contains(b, "A human approved the call to create_page"):
			return calls("complete_task", map[string]any{"summary": "done: " + b[strings.Index(b, "page created"):][:40]})
		case len(res) == 0:
			// The bare MCP tool name resolves to notes__search_notes.
			return calls("search_notes", map[string]any{"title": "launch"})
		default:
			return calls("notes__create_page", map[string]any{"title": "Launch plan"})
		}
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Write launch notes", "assignee_agent_id": researcher.ID}, &task)
	e.crew.RunPending(ctx, 1)

	if !slices.Contains(seenTools, "notes__search_notes") || !slices.Contains(seenTools, "notes__wipe_workspace") || slices.Contains(seenTools, "notes__secret_tool") {
		t.Fatalf("tools offered: %v", seenTools)
	}
	got, _ := e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskWaitingHuman || created.Load() != 0 {
		t.Fatalf("an ask tool must pause before running: %s created=%d", got.Status, created.Load())
	}
	var approvals []store.ToolApproval
	e.do("GET", "/tasks/"+task.ID.String()+"/approvals", nil, &approvals)
	if len(approvals) != 1 || approvals[0].Status != "pending" || approvals[0].Tool != "create_page" {
		t.Fatalf("approvals: %+v", approvals)
	}
	var msgs []store.TaskMessage
	e.do("GET", "/tasks/"+task.ID.String()+"/messages", nil, &msgs)
	var thread []string
	for _, m := range msgs {
		thread = append(thread, m.Kind+":"+m.Content)
	}
	joined := strings.Join(thread, "\n")
	if !strings.Contains(joined, "launch is Friday") || !strings.Contains(joined, "approval:Wants to call create_page on Notes.") {
		t.Fatalf("thread:\n%s", joined)
	}

	path := "/tasks/" + task.ID.String() + "/approvals/" + approvals[0].ID.String()
	var decided store.ToolApproval
	if code := e.do("POST", path, map[string]any{"decision": "approve"}, &decided); code != 200 || decided.Status != "approved" {
		t.Fatalf("approve: %d %+v", code, decided)
	}
	// The call runs out of band: the answer does not wait for it. RunApproval records the result
	// first and only then moves the task WAITING_HUMAN → TODO, so wait for the resume (which implies
	// the result), not for the result: RunPending below claims nothing but TODO tasks.
	for i := 0; i < 100; i++ {
		got, _ = e.store.GetTask(ctx, task.ID)
		if got.Status == store.TaskTodo {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got.Status != store.TaskTodo {
		t.Fatalf("task not resumed after approval: %s (created=%d)", got.Status, created.Load())
	}
	e.do("GET", "/tasks/"+task.ID.String()+"/approvals", nil, &approvals)
	if !strings.Contains(approvals[0].Result, "page created") {
		t.Fatalf("result after approval: %+v", approvals[0])
	}
	if code := e.do("POST", path, map[string]any{"decision": "reject"}, nil); code != 400 {
		t.Fatalf("second decision accepted: %d", code)
	}
	if created.Load() != 1 {
		t.Fatalf("approved call ran %d times", created.Load())
	}
	e.crew.RunPending(ctx, 2)
	got, _ = e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskReview || !strings.Contains(got.Result, "page created") {
		t.Fatalf("resume after approval: %s %q", got.Status, got.Result)
	}
}

type recordQueue struct {
	mu   sync.Mutex
	jobs []string
}

func (q *recordQueue) Enqueue(_ context.Context, kind jobs.Kind, id uuid.UUID, _ time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, string(kind)+"/"+id.String())
	return nil
}

// With a queue (the cloud), approving only records the decision and queues the call, so the
// request that carries the answer is never held up by a long tool.
func TestApprovedCallRunsFromTheQueue(t *testing.T) {
	e, _ := crewSetup(t)
	ctx := context.Background()
	var created atomic.Int32
	ts := notesServer(t, &created)
	var conn store.Connector
	e.do("POST", "/connectors", map[string]any{"name": "Notes", "type": "mcp_server", "config": map[string]any{"endpoint_url": ts.URL}}, &conn)
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "x"}, &task)
	ap, err := e.store.CreateToolApproval(ctx, &store.ToolApproval{TaskID: task.ID, ConnectorID: &conn.ID, ServerName: "Notes",
		Tool: "create_page", Arguments: []byte(`{"title":"t"}`)})
	if err != nil {
		t.Fatal(err)
	}
	q := &recordQueue{}
	e.crew.Queue = q
	t.Cleanup(func() { e.crew.Queue = nil })

	var decided store.ToolApproval
	if code := e.do("POST", "/tasks/"+task.ID.String()+"/approvals/"+ap.ID.String(), map[string]any{"decision": "approve"}, &decided); code != 200 ||
		decided.Status != "approved" || decided.Result != "" {
		t.Fatalf("approve: %d %+v", code, decided)
	}
	if len(q.jobs) != 1 || q.jobs[0] != "approval/"+ap.ID.String() || created.Load() != 0 {
		t.Fatalf("queued %v, tool ran %d times", q.jobs, created.Load())
	}
	// The delivery runs the call; a duplicate delivery does nothing.
	for range 2 {
		if err := e.crew.RunApproval(ctx, ap.ID); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := e.store.GetToolApproval(ctx, ap.ID)
	if created.Load() != 1 || !strings.Contains(got.Result, "page created") || got.IsError {
		t.Fatalf("ran %d times: %+v", created.Load(), got)
	}
	if err := e.crew.RunApproval(ctx, uuid.New()); err != nil {
		t.Fatalf("unknown approval must be acknowledged: %v", err)
	}
}
