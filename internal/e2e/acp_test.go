package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/mcpx"
	"github.com/j75689/cueflow/internal/store"
)

// The test binary doubles as a fake ACP agent: when CUEFLOW_FAKE_ACP=1 it speaks ACP on stdio
// instead of running tests. CueFlow launches it through an "acp_agent" runtime connector.
func TestMain(m *testing.M) {
	if os.Getenv("CUEFLOW_FAKE_ACP") == "1" {
		runFakeACPAgent()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fakeAgent struct {
	conn      *acp.AgentSideConnection
	mu        sync.Mutex
	mcpURL    string
	cancelled chan struct{}
	once      sync.Once
}

func runFakeACPAgent() {
	a := &fakeAgent{cancelled: make(chan struct{})}
	a.conn = acp.NewAgentSideConnection(a, os.Stdout, os.Stdin)
	<-a.conn.Done()
}

func (a *fakeAgent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber,
		AgentInfo:         &acp.Implementation{Name: "fake-acp-agent", Version: "1.0"},
		AgentCapabilities: acp.AgentCapabilities{McpCapabilities: acp.McpCapabilities{Http: true}},
		AuthMethods:       []acp.AuthMethod{}}, nil
}

func (a *fakeAgent) NewSession(_ context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if len(p.McpServers) != 1 || p.McpServers[0].Http == nil {
		return acp.NewSessionResponse{}, fmt.Errorf("expected one HTTP MCP server, got %+v", p.McpServers)
	}
	// FAKE_ACP_FORBID is a secret the agent must not be given: not in its environment or its MCP server.
	if v := os.Getenv("FAKE_ACP_FORBID"); v != "" {
		if len(p.McpServers[0].Http.Headers) > 0 || strings.Contains(p.McpServers[0].Http.Url, v) {
			return acp.NewSessionResponse{}, fmt.Errorf("the MCP server carries a secret")
		}
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "FAKE_ACP_FORBID=") && strings.Contains(kv, v) {
				return acp.NewSessionResponse{}, fmt.Errorf("the environment carries a secret")
			}
		}
	}
	a.mu.Lock()
	a.mcpURL = p.McpServers[0].Http.Url
	a.mu.Unlock()
	return acp.NewSessionResponse{SessionId: "fake-session"}, nil
}

func (a *fakeAgent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	text := ""
	if len(p.Prompt) > 0 && p.Prompt[0].Text != nil {
		text = p.Prompt[0].Text.Text
	}
	say := func(s string) {
		_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: acp.TextBlock(s)}}})
	}
	say("Reading the task. ")
	// CUEFLOW_FAKE_ACP_DELAY makes the agent take its time, like real work that outlives a delivery.
	if d, _ := time.ParseDuration(os.Getenv("CUEFLOW_FAKE_ACP_DELAY")); d > 0 {
		time.Sleep(d)
	}
	if !strings.Contains(text, "Write about ACP") || !strings.Contains(text, `MCP server named "cueflow"`) {
		say("The prompt did not contain the task brief.")
	}

	// 1. Try a shell command: CueFlow must refuse it.
	execKind := acp.ToolKindExecute
	title := "Bash: rm -rf /"
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.SessionUpdate{
		ToolCall: &acp.SessionUpdateToolCall{ToolCallId: "t1", Title: title, Kind: execKind, Status: acp.ToolCallStatusPending}}})
	perm, err := a.conn.RequestPermission(ctx, acp.RequestPermissionRequest{SessionId: p.SessionId,
		ToolCall: acp.ToolCallUpdate{ToolCallId: "t1", Title: &title, Kind: &execKind},
		Options: []acp.PermissionOption{
			{OptionId: "allow", Name: "Allow", Kind: acp.PermissionOptionKindAllowOnce},
			{OptionId: "reject", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce}}})
	refused := err == nil && perm.Outcome.Selected != nil && perm.Outcome.Selected.OptionId == "reject"
	failed := acp.ToolCallStatusFailed
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.SessionUpdate{
		ToolCallUpdate: &acp.SessionToolCallUpdate{ToolCallId: "t1", Status: &failed}}})

	// 2. Do the work through CueFlow's MCP tools.
	a.mu.Lock()
	url := a.mcpURL
	a.mu.Unlock()
	cs, err := mcpx.Connect(ctx, mcpx.Options{Transport: mcpx.TransportStreamable, Endpoint: url, HTTPClient: &http.Client{}})
	if err != nil {
		say("mcp connect failed: " + err.Error())
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}
	defer cs.Close()
	call := func(name string, args map[string]any) string {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return "error: " + err.Error()
		}
		if len(res.Content) > 0 {
			if t, ok := res.Content[0].(*mcp.TextContent); ok {
				return t.Text
			}
		}
		return ""
	}
	draft := call("create_post_draft", map[string]any{"platform": "threads", "title": "ACP", "content": "ACP lets CueFlow drive any coding agent."})
	call("complete_task", map[string]any{"summary": fmt.Sprintf("Done via ACP. bash_refused=%v. %s", refused, draft)})

	// CueFlow cancels the session once a terminal tool was called.
	select {
	case <-a.cancelled:
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	case <-time.After(10 * time.Second):
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}
}

func (a *fakeAgent) Cancel(context.Context, acp.CancelNotification) error {
	a.once.Do(func() { close(a.cancelled) })
	return nil
}
func (a *fakeAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}
func (a *fakeAgent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}
func (a *fakeAgent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}
func (a *fakeAgent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, nil
}
func (a *fakeAgent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, nil
}
func (a *fakeAgent) SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, nil
}
func (a *fakeAgent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, nil
}

func TestExternalACPAgentRun(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.crew.MCPBaseURL = e.api.URL

	var rt store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "Fake agent", "type": "acp_agent", "config": map[string]any{
		"preset": "custom", "command": os.Args[0], "env": map[string]any{"CUEFLOW_FAKE_ACP": "1"}, "timeout_minutes": 1,
	}}, &rt); code != 201 {
		t.Fatalf("create runtime connector: %d", code)
	}
	var test struct {
		OK      bool
		Message string
	}
	e.do("POST", "/connectors/"+rt.ID.String()+"/test", nil, &test)
	if !test.OK || !strings.Contains(test.Message, "fake-acp-agent") || !strings.Contains(test.Message, "MCP over http") {
		t.Fatalf("runtime test: %+v", test)
	}

	var agent store.Agent
	if code := e.do("POST", "/agents", map[string]any{"name": "Remote writer", "slug": "remote-writer", "capabilities": []string{"post_deliverables"},
		"runtime_connector_id": rt.ID}, &agent); code != 201 {
		t.Fatalf("create agent: %d", code)
	}
	if agent.RuntimeConnectorID == nil || *agent.RuntimeConnectorID != rt.ID {
		t.Fatalf("runtime not stored: %+v", agent)
	}

	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Write about ACP", "assignee_agent_id": agent.ID}, &task)
	e.crew.RunPending(ctx, 3)

	got, _ := e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskReview || !strings.Contains(got.Result, "bash_refused=true") {
		t.Fatalf("task: %s %q", got.Status, got.Result)
	}
	posts, _ := e.ds.ForTask(ctx, task.ID)
	if len(posts) != 1 || posts[0].Status != deliverable.StatusPendingReview {
		t.Fatalf("draft not created through MCP: %+v", posts)
	}

	var msgs []store.TaskMessage
	e.do("GET", "/tasks/"+task.ID.String()+"/messages", nil, &msgs)
	var thread []string
	for _, m := range msgs {
		thread = append(thread, m.Kind+":"+m.Content+":"+string(m.Data))
	}
	joined := strings.Join(thread, "\n")
	for _, want := range []string{
		"message:Reading the task.",            // streamed agent text
		`Refused \"Bash: rm -rf /\" (execute)`, // permission policy
		`"tool":"create_post_draft"`,           // CueFlow tool call logged by the MCP server
		"result:Done via ACP.",
	} {
		if !strings.Contains(strings.ReplaceAll(joined, `\"`, `"`), strings.ReplaceAll(want, `\"`, `"`)) {
			t.Errorf("thread missing %q:\n%s", want, joined)
		}
	}

	var runs []store.AgentRun
	e.do("GET", "/tasks/"+task.ID.String()+"/runs", nil, &runs)
	if len(runs) != 1 || runs[0].Status != "SUCCEEDED" {
		raw, _ := json.Marshal(runs)
		t.Fatalf("runs: %s", raw)
	}
	if len(e.mock.Posts()) != 0 {
		t.Fatal("an external agent must never publish")
	}

	// The per-run MCP endpoint is gone once the run ended.
	resp, err := http.Post(e.api.URL+"/mcp/run/whatever", "application/json", strings.NewReader("{}"))
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown run token should 404: %v %v", err, resp)
	}
	resp.Body.Close()
}
