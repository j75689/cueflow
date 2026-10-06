// Package acpdrive runs one prompt through an external ACP agent (Claude Code, Codex, ...) with
// CueFlow's sandbox policy: only CueFlow's MCP tools, web fetch/search (when allowed) and thinking
// are permitted; shell, file reads/edits and everything else touching the host is refused.
// It is shared by the server (agents run on the CueFlow host) and by runners (agents run on a
// registered machine); each supplies a Sink that receives the agent's activity.
package acpdrive

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
)

// ServerName is how CueFlow's tool server appears to external agents (tools become
// mcp__cueflow__<tool> in Claude Code).
const ServerName = "cueflow"

// Sink receives what the agent does. Kinds: "message", "tool_call", "tool_result".
type Sink interface {
	Say(ctx context.Context, kind, content string, data any)
	// System records a note from CueFlow itself (e.g. a refused action).
	System(ctx context.Context, text string)
}

type Params struct {
	Launch     connector.RuntimeLaunch
	AuthMethod string
	AllowWeb   bool
	// MCPURL is CueFlow's per-run tool server.
	MCPURL string
	Prompt string
	// Ended is closed when a terminal CueFlow tool finished the task; the agent is then stopped.
	Ended <-chan struct{}
	Sink  Sink
	// CredentialHint is appended to authentication failures.
	CredentialHint string
}

type Result struct {
	// AgentName is "name version" as reported by the agent on initialize.
	AgentName  string
	StopReason acp.StopReason
	FinalText  string
	ToolCalls  int
	Usage      *acp.Usage
	// Err is a failure with the step and hints already in the message.
	Err      error
	TimedOut bool
	// Canceled means ctx ended before the agent finished.
	Canceled bool
}

// Run drives the agent to completion, cancellation or timeout. It always returns a Result.
func Run(ctx context.Context, p Params) Result {
	var res Result
	dir, err := os.MkdirTemp("", "cueflow-agent-*")
	if err != nil {
		res.Err = err
		return res
	}
	defer os.RemoveAll(dir)

	timeout := p.Launch.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	client := &client{sink: p.Sink, allowWeb: p.AllowWeb}
	proc, err := acpx.Start(p.Launch, dir, client)
	if err != nil {
		res.Err = err
		return res
	}
	defer proc.Close(5 * time.Second)
	fail := func(step string, err error) {
		msg := fmt.Sprintf("%s: %v", step, err)
		if strings.Contains(strings.ToLower(err.Error()), "auth") {
			if p.CredentialHint != "" {
				msg += " — " + p.CredentialHint
			} else {
				msg += " — check the credentials on the runtime connector"
			}
		}
		if tail := proc.Stderr(); tail != "" {
			msg += " — agent stderr: " + clip(tail, 600)
		}
		res.Err = fmt.Errorf("%s", msg)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	init, err := acpx.Handshake(runCtx, proc.Conn)
	if err != nil {
		fail("ACP initialize", err)
		return res
	}
	if init.AgentInfo != nil {
		res.AgentName = strings.TrimSpace(init.AgentInfo.Name + " " + init.AgentInfo.Version)
	}
	if p.AuthMethod != "" {
		if _, err := proc.Conn.Authenticate(runCtx, acp.AuthenticateRequest{MethodId: p.AuthMethod}); err != nil {
			fail("ACP authenticate", err)
			return res
		}
	}
	var server acp.McpServer
	if init.AgentCapabilities.McpCapabilities.Http {
		server = acp.McpServer{Http: &acp.McpServerHttpInline{Type: "http", Name: ServerName, Url: p.MCPURL, Headers: []acp.HttpHeader{}}}
	} else {
		// Every ACP agent supports stdio MCP; bridge it to the HTTP endpoint.
		exe, _ := os.Executable()
		server = acp.McpServer{Stdio: &acp.McpServerStdio{Name: ServerName, Command: exe, Args: []string{"mcp-bridge", p.MCPURL}, Env: []acp.EnvVariable{}}}
	}
	sess, err := proc.Conn.NewSession(runCtx, acp.NewSessionRequest{Cwd: dir, McpServers: []acp.McpServer{server}})
	if err != nil {
		fail("ACP session/new", err)
		return res
	}
	client.setSession(string(sess.SessionId))

	type result struct {
		resp acp.PromptResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := proc.Conn.Prompt(context.WithoutCancel(runCtx), acp.PromptRequest{SessionId: sess.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(p.Prompt)}})
		done <- result{resp, err}
	}()

	cancelled := false
	cancelSession := func() {
		if !cancelled {
			cancelled = true
			_ = proc.Conn.Cancel(context.WithoutCancel(runCtx), acp.CancelNotification{SessionId: sess.SessionId})
		}
	}
	var r result
wait:
	for {
		select {
		case r = <-done:
			break wait
		case <-p.Ended:
			cancelSession()
			select {
			case r = <-done:
			case <-time.After(15 * time.Second):
			}
			break wait
		case <-runCtx.Done():
			res.TimedOut = ctx.Err() == nil
			cancelSession()
			select {
			case r = <-done:
			case <-time.After(15 * time.Second):
			}
			break wait
		case <-proc.Exited():
			r = result{err: fmt.Errorf("agent process exited")}
			break wait
		}
	}
	client.flush(ctx)
	res.ToolCalls = client.toolCalls()
	res.Usage = r.resp.Usage
	res.StopReason = r.resp.StopReason
	res.FinalText = client.finalText()
	switch {
	case ctx.Err() != nil:
		res.Canceled = true
	case res.TimedOut:
	case r.err != nil:
		fail("ACP prompt", r.err)
	}
	return res
}

// client handles agent->client traffic for one run: it streams the agent's messages and tool
// activity to the sink and decides permission requests.
type client struct {
	acpx.RejectingClient
	sink     Sink
	allowWeb bool

	mu        sync.Mutex
	session   string
	text      strings.Builder // current unflushed agent message
	last      string          // last flushed agent message (the result on end_turn)
	calls     int
	toolTitle map[string]string
}

func (a *client) setSession(id string) {
	a.mu.Lock()
	a.session = id
	a.mu.Unlock()
}

func (a *client) toolCalls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func (a *client) finalText() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

// flush writes buffered agent text to the sink as one message.
func (a *client) flush(ctx context.Context) {
	a.mu.Lock()
	text := strings.TrimSpace(a.text.String())
	a.text.Reset()
	if text != "" {
		a.last = text
	}
	a.mu.Unlock()
	if text != "" {
		a.sink.Say(ctx, "message", text, nil)
	}
}

func isCueflowTool(title string) bool {
	t := strings.ToLower(title)
	return strings.Contains(t, "mcp__"+ServerName+"__") || strings.HasPrefix(t, ServerName+".") ||
		strings.HasPrefix(t, ServerName+"/") || strings.HasPrefix(t, ServerName+":") ||
		strings.HasPrefix(t, ServerName+"_") // Google Antigravity: cueflow_complete_task
}

// hostTouching kinds act on the machine; a title alone never earns them a pass.
func hostTouching(kind acp.ToolKind) bool {
	switch kind {
	case acp.ToolKindExecute, acp.ToolKindEdit, acp.ToolKindDelete, acp.ToolKindMove:
		return true
	}
	return false
}

// allowedTool decides whether a permission request is for one of CueFlow's own tools (or a
// harmless kind). The "cueflow_" naming is not a documented convention, so it is only trusted for
// tools that do not claim to touch the host.
func allowedTool(title string, kind acp.ToolKind, allowWeb bool) bool {
	if isCueflowTool(title) {
		underscoreOnly := strings.HasPrefix(strings.ToLower(title), ServerName+"_")
		if !underscoreOnly || !hostTouching(kind) {
			return true
		}
	}
	return kind == acp.ToolKindThink || (allowWeb && kind == acp.ToolKindFetch)
}

func (a *client) SessionUpdate(ctx context.Context, n acp.SessionNotification) error {
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
			a.sink.Say(ctx, "tool_call", "", map[string]any{
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
		a.sink.Say(ctx, "tool_result", clip(toolOutputText(tu), 2000),
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
func (a *client) RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
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
	if allowedTool(title, kind, a.allowWeb) {
		if out, ok := acpx.AllowOutcome(p.Options); ok {
			return acp.RequestPermissionResponse{Outcome: out}, nil
		}
	}
	a.sink.System(ctx, fmt.Sprintf("Refused %q (%s) for the external agent: only CueFlow tools%s are permitted.",
		title, kind, map[bool]string{true: " and web fetch/search", false: ""}[a.allowWeb]))
	return acp.RequestPermissionResponse{Outcome: acpx.RejectOutcome(p.Options)}, nil
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if n > 0 && len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
