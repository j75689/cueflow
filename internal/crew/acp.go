package crew

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/j75689/cueflow/internal/acpx"
	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/store"
)

// mcpServerName is how CueFlow's tool server appears to external agents (tools become
// mcp__cueflow__<tool> in Claude Code).
const mcpServerName = "cueflow"

const acpPrompt = `You are working inside CueFlow as the agent described below. CueFlow is a self-hosted social media
operations console. Your tools for this task are provided by the MCP server named "cueflow"
(e.g. cueflow.create_post_draft, cueflow.add_comment, cueflow.ask_human, cueflow.complete_task).

Rules for this environment:
- Do all task work through the "cueflow" tools. Shell commands, file edits and local file access are
  not available and will be refused; do not try them.
- Web fetch/search tools may be available for research.
- You cannot publish to social media. Drafts go to a human review queue.
- Finish by calling cueflow.complete_task with a concise summary, or cueflow.ask_human if you need a decision.

`

type runtimeOptions interface {
	AuthMethod() string
	AllowWeb() bool
}

// acpLoop runs one task through an external ACP agent. It mirrors loop(): same tools (served over
// MCP), same thread logging, same outcomes.
func (c *Crew) acpLoop(ctx context.Context, task *store.Task, a *store.Agent, run *store.AgentRun) *outcome {
	inst, conn, err := c.hub.Instance(ctx, *a.RuntimeConnectorID, false)
	if err != nil {
		return &outcome{kind: "error", text: "runtime connector: " + err.Error()}
	}
	defer hub.Close(inst)
	rt, ok := inst.(connector.Runtime)
	if !ok {
		return &outcome{kind: "error", text: fmt.Sprintf("connector %q is not an agent runtime", conn.Name)}
	}
	launch := rt.Launch()
	allowWeb := true
	authMethod := ""
	if o, ok := inst.(runtimeOptions); ok {
		allowWeb, authMethod = o.AllowWeb(), o.AuthMethod()
	}
	if launch.Timeout <= 0 {
		launch.Timeout = 20 * time.Minute
	}
	if c.MCPBaseURL == "" {
		return &outcome{kind: "error", text: "MCP base URL is not configured"}
	}

	env := &runEnv{c: c, task: task, agent: a}
	token, mrun, unregister := c.registerMCPRun(env)
	defer unregister()
	defer func() {
		if env.searcher != nil {
			hub.Close(env.searcher)
		}
	}()

	dir, err := os.MkdirTemp("", "cueflow-agent-*")
	if err != nil {
		return &outcome{kind: "error", text: err.Error()}
	}
	defer os.RemoveAll(dir)

	client := &acpClient{c: c, task: task, agent: a, allowWeb: allowWeb, run: run}
	proc, err := acpx.Start(launch, dir, client)
	if err != nil {
		return &outcome{kind: "error", text: err.Error()}
	}
	defer proc.Close(5 * time.Second)
	fail := func(step string, err error) *outcome {
		msg := fmt.Sprintf("%s: %v", step, err)
		if tail := proc.Stderr(); tail != "" {
			msg += " — agent stderr: " + clip(tail, 600)
		}
		return &outcome{kind: "error", text: msg}
	}

	runCtx, cancel := context.WithTimeout(ctx, launch.Timeout)
	defer cancel()

	init, err := acpx.Handshake(runCtx, proc.Conn)
	if err != nil {
		return fail("ACP initialize", err)
	}
	if authMethod != "" {
		if _, err := proc.Conn.Authenticate(runCtx, acp.AuthenticateRequest{MethodId: authMethod}); err != nil {
			return fail("ACP authenticate", err)
		}
	}
	url := strings.TrimRight(c.MCPBaseURL, "/") + MCPPathPrefix + token
	var server acp.McpServer
	if init.AgentCapabilities.McpCapabilities.Http {
		server = acp.McpServer{Http: &acp.McpServerHttpInline{Type: "http", Name: mcpServerName, Url: url, Headers: []acp.HttpHeader{}}}
	} else {
		// Every ACP agent supports stdio MCP; bridge it to the HTTP endpoint.
		exe, _ := os.Executable()
		server = acp.McpServer{Stdio: &acp.McpServerStdio{Name: mcpServerName, Command: exe, Args: []string{"mcp-bridge", url}, Env: []acp.EnvVariable{}}}
	}
	sess, err := proc.Conn.NewSession(runCtx, acp.NewSessionRequest{Cwd: dir, McpServers: []acp.McpServer{server}})
	if err != nil {
		return fail("ACP session/new", err)
	}
	client.setSession(string(sess.SessionId))

	team, _ := c.store.ListAgents(ctx)
	var memories []store.AgentMemory
	if a.Has("memory") {
		memories, _ = c.store.Memories(ctx, a.ID, "", 30)
	}
	prompt := acpPrompt + buildSystemPrompt(a, task, team, memories) + "\n\n---\n\n" + c.buildBrief(ctx, task)

	type result struct {
		resp acp.PromptResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := proc.Conn.Prompt(context.WithoutCancel(runCtx), acp.PromptRequest{SessionId: sess.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(prompt)}})
		done <- result{resp, err}
	}()

	cancelled := false
	cancelSession := func() {
		if !cancelled {
			cancelled = true
			_ = proc.Conn.Cancel(context.WithoutCancel(runCtx), acp.CancelNotification{SessionId: sess.SessionId})
		}
	}
	var res result
	timedOut := false
wait:
	for {
		select {
		case res = <-done:
			break wait
		case <-mrun.done:
			// A terminal tool (complete_task / ask_human / wait_for_subtasks) ended the task; stop the agent.
			cancelSession()
			select {
			case res = <-done:
			case <-time.After(15 * time.Second):
			}
			break wait
		case <-runCtx.Done():
			timedOut = ctx.Err() == nil
			cancelSession()
			select {
			case res = <-done:
			case <-time.After(15 * time.Second):
			}
			break wait
		case <-proc.Exited():
			res = result{err: fmt.Errorf("agent process exited")}
			break wait
		}
	}
	client.flush(ctx)
	run.Turns = max(run.Turns, 1)
	run.ToolCalls += client.toolCalls()
	if u := res.resp.Usage; u != nil {
		run.InputTokens += int64(u.InputTokens)
		run.OutputTokens += int64(u.OutputTokens)
	}

	if env.outcome != nil {
		return env.outcome
	}
	switch {
	case ctx.Err() != nil:
		return &outcome{kind: "canceled"}
	case timedOut:
		return &outcome{kind: "error", text: fmt.Sprintf("the external agent did not finish within %s", launch.Timeout)}
	case res.err != nil:
		return fail("ACP prompt", res.err)
	}
	switch res.resp.StopReason {
	case acp.StopReasonEndTurn:
		text := strings.TrimSpace(client.finalText())
		if text == "" {
			text = "(finished without a summary)"
		}
		return &outcome{kind: "complete", text: text}
	case acp.StopReasonMaxTurnRequests, acp.StopReasonMaxTokens:
		return &outcome{kind: "limit"}
	case acp.StopReasonRefusal:
		return &outcome{kind: "error", text: "the external agent refused the request"}
	case acp.StopReasonCancelled:
		return &outcome{kind: "canceled"}
	}
	return &outcome{kind: "error", text: fmt.Sprintf("unexpected stop reason %q", res.resp.StopReason)}
}

// acpClient handles agent->client traffic for one run: it streams the agent's messages and tool
// activity into the task thread and decides permission requests.
type acpClient struct {
	acpx.RejectingClient
	c        *Crew
	task     *store.Task
	agent    *store.Agent
	run      *store.AgentRun
	allowWeb bool

	mu        sync.Mutex
	session   string
	text      strings.Builder // current unflushed agent message
	last      string          // last flushed agent message (used as the result on end_turn)
	calls     int
	toolTitle map[string]string
}

func (a *acpClient) setSession(id string) {
	a.mu.Lock()
	a.session = id
	a.mu.Unlock()
}

func (a *acpClient) toolCalls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func (a *acpClient) finalText() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

// flush writes buffered agent text to the thread as one message.
func (a *acpClient) flush(ctx context.Context) {
	a.mu.Lock()
	text := strings.TrimSpace(a.text.String())
	a.text.Reset()
	if text != "" {
		a.last = text
	}
	a.mu.Unlock()
	if text != "" {
		a.c.say(ctx, a.task.ID, a.agent, "message", text, nil)
	}
}

func isCueflowTool(title string) bool {
	t := strings.ToLower(title)
	return strings.Contains(t, "mcp__"+mcpServerName+"__") || strings.HasPrefix(t, mcpServerName+".") ||
		strings.HasPrefix(t, mcpServerName+"/") || strings.HasPrefix(t, mcpServerName+":")
}

func (a *acpClient) SessionUpdate(ctx context.Context, n acp.SessionNotification) error {
	u := n.Update
	switch {
	case u.AgentMessageChunk != nil:
		if t := u.AgentMessageChunk.Content.Text; t != nil {
			a.mu.Lock()
			a.text.WriteString(t.Text)
			a.mu.Unlock()
		}
	case u.ToolCall != nil:
		a.flush(ctx)
		tc := u.ToolCall
		a.mu.Lock()
		a.calls++
		if a.toolTitle == nil {
			a.toolTitle = map[string]string{}
		}
		a.toolTitle[string(tc.ToolCallId)] = tc.Title
		a.mu.Unlock()
		// CueFlow's own tools are logged by the MCP server; log only the agent's built-in tools.
		if !isCueflowTool(tc.Title) {
			a.c.say(ctx, a.task.ID, a.agent, "tool_call", "", map[string]any{
				"tool": tc.Title, "kind": string(tc.Kind), "arguments": tc.RawInput, "external": true})
		}
	case u.ToolCallUpdate != nil:
		tu := u.ToolCallUpdate
		if tu.Status == nil || (*tu.Status != acp.ToolCallStatusCompleted && *tu.Status != acp.ToolCallStatusFailed) {
			return nil
		}
		a.mu.Lock()
		title := a.toolTitle[string(tu.ToolCallId)]
		a.mu.Unlock()
		if tu.Title != nil {
			title = *tu.Title
		}
		if isCueflowTool(title) {
			return nil
		}
		failed := *tu.Status == acp.ToolCallStatusFailed
		a.c.say(ctx, a.task.ID, a.agent, "tool_result", clip(toolOutputText(tu), 2000),
			map[string]any{"tool": title, "is_error": failed, "external": true})
	}
	return nil
}

func toolOutputText(tu *acp.SessionToolCallUpdate) string {
	var parts []string
	for _, c := range tu.Content {
		if c.Content != nil && c.Content.Content.Text != nil {
			parts = append(parts, c.Content.Content.Text.Text)
		}
	}
	if len(parts) == 0 && tu.RawOutput != nil {
		if b, err := json.Marshal(tu.RawOutput); err == nil {
			return string(b)
		}
	}
	if len(parts) == 0 {
		return string(*tu.Status)
	}
	return strings.Join(parts, "\n")
}

// RequestPermission is the external agent's sandbox policy: CueFlow's MCP tools are allowed (they
// carry their own checks and can never publish); web fetch/search and thinking are allowed when
// enabled; everything touching the host (shell, file reads/edits, deletes, moves) is refused.
func (a *acpClient) RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	title := ""
	if p.ToolCall.Title != nil {
		title = *p.ToolCall.Title
	}
	if title == "" {
		a.mu.Lock()
		title = a.toolTitle[string(p.ToolCall.ToolCallId)]
		a.mu.Unlock()
	}
	kind := acp.ToolKindOther
	if p.ToolCall.Kind != nil {
		kind = *p.ToolCall.Kind
	}
	allowed := isCueflowTool(title) || kind == acp.ToolKindThink ||
		(a.allowWeb && kind == acp.ToolKindFetch)
	if allowed {
		if out, ok := acpx.AllowOutcome(p.Options); ok {
			return acp.RequestPermissionResponse{Outcome: out}, nil
		}
	}
	a.c.system(ctx, a.task.ID, fmt.Sprintf("Refused %q (%s) for the external agent: only CueFlow tools%s are permitted.",
		title, kind, map[bool]string{true: " and web fetch/search", false: ""}[a.allowWeb]))
	return acp.RequestPermissionResponse{Outcome: acpx.RejectOutcome(p.Options)}, nil
}
