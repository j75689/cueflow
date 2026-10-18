package e2e

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/store"
)

const testMCPToken = "test-mcp-token"

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func mcpSession(t *testing.T, e *env) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: e.api.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{testMCPToken}}, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

func TestOperatorMCP(t *testing.T) {
	e, agents := crewSetup(t)
	ctx := context.Background()

	resp, err := http.Post(e.api.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("request without token: %d", resp.StatusCode)
	}

	cs := mcpSession(t, e)
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
		if strings.Contains(tl.Name, "approve") || strings.Contains(tl.Name, "publish") {
			t.Fatalf("operator MCP must not expose %s", tl.Name)
		}
	}
	if len(names) != 11 {
		t.Fatalf("tools: %v", names)
	}

	if out, isErr := callTool(t, cs, "list_agents", nil); isErr || !strings.Contains(out, `"writer"`) {
		t.Fatalf("list_agents: %v %s", isErr, out)
	}
	if out, isErr := callTool(t, cs, "create_task", map[string]any{"title": "x", "assignee": "nobody"}); !isErr || !strings.Contains(out, "no agent") {
		t.Fatalf("unknown assignee: %v %s", isErr, out)
	}

	out, isErr := callTool(t, cs, "create_task", map[string]any{"title": "Post about MCP", "assignee": "@writer", "status": "backlog"})
	if isErr || !strings.Contains(out, `"BACKLOG"`) {
		t.Fatalf("create_task: %s", out)
	}
	tasks, _ := e.store.ListTasks(ctx, store.TaskFilter{All: true})
	if len(tasks) != 1 || tasks[0].AssigneeAgentID == nil || *tasks[0].AssigneeAgentID != agents["writer"].ID || tasks[0].CreatedBy != "human" {
		t.Fatalf("stored task: %+v", tasks)
	}
	ref := "#" + strconv.FormatInt(tasks[0].Number, 10)

	if out, isErr := callTool(t, cs, "update_task", map[string]any{"task": ref, "status": "IN_PROGRESS"}); !isErr {
		t.Fatalf("runner-owned status must be refused: %s", out)
	}
	if out, isErr := callTool(t, cs, "update_task", map[string]any{"task": ref, "status": "REVIEW", "priority": "high"}); isErr || !strings.Contains(out, `"high"`) {
		t.Fatalf("update_task: %s", out)
	}
	// A comment on a task in REVIEW sends it back to the agent.
	if out, isErr := callTool(t, cs, "comment_task", map[string]any{"task": ref, "content": "Make it shorter"}); isErr || !strings.Contains(out, `"TODO"`) {
		t.Fatalf("comment_task: %s", out)
	}
	out, isErr = callTool(t, cs, "get_task", map[string]any{"task": tasks[0].ID.String()})
	if isErr || !strings.Contains(out, "Make it shorter") || !strings.Contains(out, "Moved to REVIEW") {
		t.Fatalf("get_task: %s", out)
	}
	if out, isErr := callTool(t, cs, "stop_task", map[string]any{"task": ref}); isErr || !strings.Contains(out, `"WAITING_HUMAN"`) {
		t.Fatalf("stop_task: %s", out)
	}
	if out, isErr := callTool(t, cs, "list_tasks", map[string]any{"assignee": "writer", "status": []string{"WAITING_HUMAN"}}); isErr || !strings.Contains(out, "Post about MCP") {
		t.Fatalf("list_tasks: %s", out)
	}
	if out, isErr := callTool(t, cs, "list_drafts", nil); isErr || !strings.Contains(out, `"total": 0`) {
		t.Fatalf("list_drafts: %s", out)
	}
	if out, isErr := callTool(t, cs, "get_task", map[string]any{"task": "#999"}); !isErr || !strings.Contains(out, "not found") {
		t.Fatalf("missing task: %s", out)
	}
}
