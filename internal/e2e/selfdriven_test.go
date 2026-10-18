package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/mcpx"
	"github.com/j75689/cueflow/internal/store"
)

func (e *env) agentMCP(ctx context.Context, token string) *mcp.ClientSession {
	e.t.Helper()
	cs, err := mcpx.Connect(ctx, mcpx.Options{Transport: mcpx.TransportStreamable, Endpoint: e.api.URL + "/mcp",
		Headers: map[string]string{"Authorization": "Bearer " + token}, HTTPClient: &http.Client{}})
	if err != nil {
		e.t.Fatalf("connect with agent token: %v", err)
	}
	e.t.Cleanup(func() { cs.Close() })
	return cs
}

func callText(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			return tc.Text, res.IsError
		}
	}
	return "", res.IsError
}

// An agent with the "external" runtime is never started by CueFlow: it claims its tasks over MCP
// with its own token and works them with the usual tools.
func TestSelfDrivenAgent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	var rt store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "Self-driven", "type": "external", "config": map[string]any{}}, &rt); code != 201 {
		t.Fatalf("create runtime: %d", code)
	}
	mk := func(name, slug string) store.Agent {
		var a store.Agent
		if code := e.do("POST", "/agents", map[string]any{"name": name, "slug": slug, "capabilities": []string{"post_deliverables"},
			"runtime_connector_id": rt.ID}, &a); code != 201 {
			t.Fatalf("create agent: %d", code)
		}
		return a
	}
	agent, other := mk("Outside writer", "outside"), mk("Other writer", "other")

	var tok struct {
		Token string `json:"token"`
	}
	if code := e.do("POST", "/agents/"+agent.ID.String()+"/tokens", map[string]any{"name": "laptop"}, &tok); code != 201 || !strings.HasPrefix(tok.Token, "cfa_") {
		t.Fatalf("create token: %d %q", code, tok.Token)
	}
	var otherTok struct {
		Token string `json:"token"`
	}
	e.do("POST", "/agents/"+other.ID.String()+"/tokens", map[string]any{"name": "x"}, &otherTok)

	var task, foreign store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Write about MCP", "assignee_agent_id": agent.ID}, &task)
	e.do("POST", "/tasks", map[string]any{"title": "Someone else's", "assignee_agent_id": other.ID}, &foreign)

	// CueFlow's own loop leaves the task alone.
	e.crew.RunPending(ctx, 2)
	if got, _ := e.store.GetTask(ctx, task.ID); got.Status != store.TaskTodo {
		t.Fatalf("a self-driven task must wait for its agent, got %s", got.Status)
	}

	cs := e.agentMCP(ctx, tok.Token)
	list, isErr := callText(t, cs, "list_my_tasks", nil)
	if isErr || !strings.Contains(list, "Write about MCP") || strings.Contains(list, "Someone else's") {
		t.Fatalf("list_my_tasks must show only this agent's tasks: %s", list)
	}
	if _, isErr := callText(t, cs, "claim_task", map[string]any{"task": foreign.ID.String()}); !isErr {
		t.Fatal("an agent must not claim another agent's task")
	}
	if _, isErr := callText(t, cs, "create_post_draft", map[string]any{"platform": "threads", "title": "x", "content": "y"}); !isErr {
		t.Fatal("tools need a claimed task first")
	}

	brief, isErr := callText(t, cs, "claim_task", nil)
	if isErr || !strings.Contains(brief, "Write about MCP") {
		t.Fatalf("claim_task: %v %s", isErr, brief)
	}
	if got, _ := e.store.GetTask(ctx, task.ID); got.Status != store.TaskInProgress {
		t.Fatalf("claimed task is %s", got.Status)
	}
	if out, isErr := callText(t, cs, "create_post_draft", map[string]any{"platform": "threads", "title": "MCP", "content": "Agents can pull their own work."}); isErr {
		t.Fatalf("create_post_draft: %s", out)
	}
	if out, isErr := callText(t, cs, "complete_task", map[string]any{"summary": "Done from outside."}); isErr {
		t.Fatalf("complete_task: %s", out)
	}

	got, _ := e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskReview || got.Result != "Done from outside." {
		t.Fatalf("task after completion: %s %q", got.Status, got.Result)
	}
	posts, _ := e.ds.ForTask(ctx, task.ID)
	if len(posts) != 1 || posts[0].Status != deliverable.StatusPendingReview {
		t.Fatalf("draft not in review: %+v", posts)
	}
	var runs []store.AgentRun
	e.do("GET", "/tasks/"+task.ID.String()+"/runs", nil, &runs)
	if len(runs) != 1 || runs[0].Status != "SUCCEEDED" || runs[0].Runtime != "external" {
		t.Fatalf("runs: %+v", runs)
	}
	if len(e.mock.Posts()) != 0 {
		t.Fatal("an external agent must never publish")
	}

	// A claim that goes quiet expires and the task returns to To do.
	var second store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Second", "assignee_agent_id": agent.ID}, &second)
	callText(t, cs, "claim_task", nil)
	if _, err := e.pool.Exec(ctx, `UPDATE tasks SET lease_until = NOW() - INTERVAL '1 minute' WHERE id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	e.crew.Every = 50 * time.Millisecond
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go e.crew.Run(runCtx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if g, _ := e.store.GetTask(ctx, second.ID); g.Status == store.TaskTodo {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if g, _ := e.store.GetTask(ctx, second.ID); g.Status != store.TaskTodo {
		t.Fatalf("expired claim should return to TODO, is %s", g.Status)
	}

	// Revoking the token cuts the agent off.
	var tokens []store.AgentToken
	e.do("GET", "/agents/"+agent.ID.String()+"/tokens", nil, &tokens)
	if len(tokens) != 1 {
		t.Fatalf("tokens: %+v", tokens)
	}
	e.do("DELETE", "/agents/"+agent.ID.String()+"/tokens/"+tokens[0].ID.String(), nil, nil)
	req, _ := http.NewRequest("POST", e.api.URL+"/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token should be refused: %v %v", err, resp)
	}
	resp.Body.Close()
}
