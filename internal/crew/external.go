package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/jobs"
	"github.com/j75689/cueflow/internal/store"
)

// External tools come from the MCP tool servers attached to an agent. Each tool's permission
// (connector.PermAllow / PermAsk / PermDeny, default from connector.DefaultPermission) decides
// whether the agent sees it, calls it directly, or must wait for a human to approve each call.

const externalCallTimeout = 2 * time.Minute

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// agentTools is everything an agent may call this run: built-in tools for its capabilities plus
// the tools of its MCP servers that are not denied.
func (c *Crew) agentTools(ctx context.Context, a *store.Agent) []tool {
	out := toolsFor(a)
	used := map[string]bool{}
	for _, t := range toolCatalog() {
		used[t.name] = true
	}
	for _, id := range a.ToolConnectorIDs {
		conn, err := c.store.GetConnector(ctx, id)
		if err != nil || !conn.IsActive || conn.Kind != connector.KindTools {
			continue
		}
		list, err := c.listExternal(ctx, id)
		if err != nil {
			c.logger.Warn("mcp tool server unavailable", "server", conn.Name, "err", err)
			continue
		}
		prefix := strings.Trim(strings.ToLower(unsafeName.ReplaceAllString(conn.Name, "_")), "_")
		if len(prefix) > 20 {
			prefix = prefix[:20]
		}
		if prefix == "" {
			prefix = "mcp"
		}
		for _, et := range list {
			perm := conn.ToolPermissions[et.Name]
			if !connector.ValidPermission(perm) {
				perm = connector.DefaultPermission(et)
			}
			if perm == connector.PermDeny {
				continue
			}
			name := prefix + "__" + unsafeName.ReplaceAllString(et.Name, "_")
			if len(name) > 60 {
				name = name[:60]
			}
			for i := 2; used[name]; i++ {
				name = fmt.Sprintf("%s_%d", strings.TrimRight(name, "0123456789_"), i)
			}
			used[name] = true
			desc := fmt.Sprintf("[%s] %s", conn.Name, et.Description)
			if perm == connector.PermAsk {
				desc += " Each call needs the human's approval: calling it pauses this task until they decide."
			}
			out = append(out, tool{name: name, description: desc, schema: et.InputSchema,
				run: c.externalRun(conn.ID, conn.Name, et.Name, perm)})
		}
	}
	return out
}

func (c *Crew) listExternal(ctx context.Context, id uuid.UUID) ([]connector.ExternalTool, error) {
	inst, _, err := c.hub.Instance(ctx, id, false)
	if err != nil {
		return nil, err
	}
	defer hub.Close(inst)
	p, ok := inst.(connector.ToolProvider)
	if !ok {
		return nil, errors.New("not a tool server")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return p.ExternalTools(ctx)
}

func (c *Crew) callExternal(ctx context.Context, id uuid.UUID, name string, args json.RawMessage) (string, bool, error) {
	inst, _, err := c.hub.Instance(ctx, id, false)
	if err != nil {
		return "", false, err
	}
	defer hub.Close(inst)
	p, ok := inst.(connector.ToolProvider)
	if !ok {
		return "", false, errors.New("not a tool server")
	}
	timeout := externalCallTimeout
	if t, ok := inst.(interface{ CallTimeout() time.Duration }); ok && t.CallTimeout() > timeout {
		timeout = t.CallTimeout()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return p.CallTool(ctx, name, args)
}

func (c *Crew) externalRun(connID uuid.UUID, server, name, perm string) func(context.Context, *runEnv, json.RawMessage) (string, error) {
	return func(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
		if perm == connector.PermAsk {
			return c.requestApproval(ctx, e, connID, server, name, raw)
		}
		text, isErr, err := c.callExternal(ctx, connID, name, raw)
		if err != nil {
			return "", err
		}
		if isErr {
			return "", errors.New(text)
		}
		return text, nil
	}
}

// requestApproval records the call, shows it to the human and ends the run until they decide.
func (c *Crew) requestApproval(ctx context.Context, e *runEnv, connID uuid.UUID, server, name string, raw json.RawMessage) (string, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		raw = json.RawMessage(`{}`)
	}
	ap, err := c.store.CreateToolApproval(ctx, &store.ToolApproval{TaskID: e.task.ID, AgentID: &e.agent.ID,
		ConnectorID: &connID, ServerName: server, Tool: name, Arguments: raw})
	if err != nil {
		return "", err
	}
	var args any = map[string]any{}
	_ = json.Unmarshal(raw, &args)
	c.say(ctx, e.task.ID, e.agent, "approval", fmt.Sprintf("Wants to call %s on %s.", name, server),
		map[string]any{"approval_id": ap.ID, "server": server, "tool": name, "arguments": args})
	e.outcome = &outcome{kind: "approval"}
	return "Approval requested. Stop now; you will be resumed with the result once the human decides.", nil
}

// DecideApproval applies a human's decision. An approved call runs now, with its result posted to
// the thread; either way the agent is resumed once no other approval is pending.
func (c *Crew) DecideApproval(ctx context.Context, taskID, approvalID uuid.UUID, approve bool, note string) (*store.ToolApproval, error) {
	ap, err := c.store.GetToolApproval(ctx, approvalID)
	if err != nil {
		return nil, err
	}
	if ap.TaskID != taskID {
		return nil, &store.ValidationError{Msg: "this approval belongs to another task"}
	}
	status := "rejected"
	if approve {
		status = "approved"
	}
	claimed, err := c.store.ClaimToolApproval(ctx, approvalID, status, strings.TrimSpace(note))
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, &store.ValidationError{Msg: "this call was already " + ap.Status}
	}
	if approve {
		// The call can take minutes (a render): the decision is recorded and the answer returned now,
		// and the call runs from the queue (or a goroutine without one).
		c.startApproval(approvalID)
		return c.store.GetToolApproval(ctx, approvalID)
	}
	noteText := ""
	if n := strings.TrimSpace(note); n != "" {
		noteText = fmt.Sprintf(" Their note: %q.", n)
	}
	c.system(ctx, taskID, fmt.Sprintf("A human rejected the call to %s on %s.%s Do not retry it; continue without it or ask the human.",
		ap.Tool, ap.ServerName, noteText))
	c.resumeAfterApproval(ctx, taskID)
	return c.store.GetToolApproval(ctx, approvalID)
}

// startApproval runs an approved call out of band: as a queued job in the cloud (a request that
// answers a browser would be cut off by the proxy long before a render ends), else in a goroutine.
func (c *Crew) startApproval(id uuid.UUID) {
	if c.Queue != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := c.Queue.Enqueue(ctx, jobs.KindApproval, id, 0); err != nil {
			// Better a slow answer than a call nobody will ever make.
			c.logger.Error("enqueue approved call, running it here", "approval", id, "err", err)
			_ = c.RunApproval(context.WithoutCancel(ctx), id)
		}
		return
	}
	go func() { _ = c.RunApproval(context.Background(), id) }()
}

// RunApproval is the jobs.KindApproval handler: it makes an approved tool call, records the result
// in the thread and resumes the agent once no other approval is pending. A call that already has a
// result (a duplicate delivery) or is not approved is left alone.
func (c *Crew) RunApproval(ctx context.Context, approvalID uuid.UUID) error {
	ap, err := c.store.GetToolApproval(ctx, approvalID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if ap.Status != "approved" || ap.Result != "" {
		return nil
	}
	noteText := ""
	if n := strings.TrimSpace(ap.Note); n != "" {
		noteText = fmt.Sprintf(" Their note: %q.", n)
	}
	text, isErr := "", false
	if ap.ConnectorID == nil {
		text, isErr = "the tool server was deleted", true
	} else {
		var cerr error
		text, isErr, cerr = c.callExternal(ctx, *ap.ConnectorID, ap.Tool, ap.Arguments)
		if cerr != nil {
			text, isErr = cerr.Error(), true
		}
	}
	if r := []rune(text); len(r) > maxToolResult {
		text = string(r[:maxToolResult]) + "… (truncated)"
	}
	if text == "" {
		text = "(no output)" // an empty result would read as "still running"
	}
	_ = c.store.SetToolApprovalResult(ctx, approvalID, text, isErr)
	outcome := "It ran and returned"
	if isErr {
		outcome = "It ran but failed with"
	}
	c.system(ctx, ap.TaskID, fmt.Sprintf("A human approved the call to %s on %s.%s %s:\n%s\nDo not call it again for this request.",
		ap.Tool, ap.ServerName, noteText, outcome, clip(text, 4000)))
	c.resumeAfterApproval(ctx, ap.TaskID)
	return nil
}

func (c *Crew) resumeAfterApproval(ctx context.Context, taskID uuid.UUID) {
	all, err := c.store.ListToolApprovals(ctx, taskID)
	if err != nil {
		return
	}
	for _, a := range all {
		if a.Status == "pending" || (a.Status == "approved" && a.Result == "") {
			return // wait for every decision, and every approved call, before resuming
		}
	}
	t, err := c.store.GetTask(ctx, taskID)
	if err != nil || t.AssigneeAgentID == nil {
		return
	}
	if ok, _ := c.store.SetTaskStatusIf(ctx, taskID, []store.TaskStatus{store.TaskWaitingHuman}, store.TaskTodo, nil); ok {
		c.broker.Publish("task.updated", map[string]any{"id": taskID, "status": store.TaskTodo})
		c.Wake()
	}
}
