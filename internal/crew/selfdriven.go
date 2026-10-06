package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/guard"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

// Self-driven agents: an external agent connects to /mcp with its agent token, claims tasks
// assigned to it, and works them with the same tools a built-in agent has. The server never
// starts it. A claim reserves the task for selfDrivenLease; every tool call extends it, and a task
// whose agent goes quiet goes back to TODO.

const selfDrivenLease = 30 * time.Minute

// isSelfDriven reports whether the agent's runtime is the "external" connector.
func (c *Crew) isSelfDriven(ctx context.Context, a *store.Agent) bool {
	if a == nil || a.RuntimeConnectorID == nil {
		return false
	}
	conn, err := c.store.GetConnector(ctx, *a.RuntimeConnectorID)
	return err == nil && conn.Type == store.ExternalRuntimeType
}

// AgentMCPServer is the tool server for one self-driven agent, acting with that agent's identity.
func (c *Crew) AgentMCPServer(a *store.Agent) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "cueflow", Version: "0.2.0"}, nil)
	add := func(name, desc string, schema map[string]any, fn func(ctx context.Context, args json.RawMessage) (string, bool)) {
		srv.AddTool(&mcp.Tool{Name: name, Description: desc, InputSchema: schema},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				args := req.Params.Arguments
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				text, isErr := fn(ctx, args)
				return &mcp.CallToolResult{IsError: isErr, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
			})
	}
	taskProp := map[string]any{"type": "string", "description": "task number (e.g. 12 or #12) or task id; optional when you have exactly one task in progress"}

	add("list_my_tasks", "List tasks assigned to you that are waiting (TODO), in progress, or waiting for a human.",
		obj(map[string]any{"status": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
			"description": "limit to these statuses (default TODO, IN_PROGRESS, WAITING_HUMAN, BLOCKED)"}}),
		func(ctx context.Context, args json.RawMessage) (string, bool) {
			var in struct {
				Status []string `json:"status"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return err.Error(), true
			}
			statuses := []store.TaskStatus{store.TaskTodo, store.TaskInProgress, store.TaskWaitingHuman, store.TaskBlocked}
			if len(in.Status) > 0 {
				statuses = nil
				for _, s := range in.Status {
					st := store.TaskStatus(strings.ToUpper(strings.TrimSpace(s)))
					if !st.Valid() {
						return fmt.Sprintf("unknown status %q", s), true
					}
					statuses = append(statuses, st)
				}
			}
			list, err := c.store.ListTasks(ctx, store.TaskFilter{OrgID: &a.OrgID, AssigneeID: &a.ID, Statuses: statuses, Limit: 50})
			if err != nil {
				return err.Error(), true
			}
			rows := make([]map[string]any, 0, len(list))
			for _, t := range list {
				rows = append(rows, map[string]any{"number": t.Number, "id": t.ID, "title": t.Title, "status": t.Status, "priority": t.Priority})
			}
			out, _ := jsonResult(rows)
			return out, false
		})

	add("claim_task", "Claim a task assigned to you (the most urgent TODO one by default) and start working on it. Returns your instructions and the task brief. The claim lasts 30 minutes and is extended by every tool call.",
		obj(map[string]any{"task": taskProp}),
		func(ctx context.Context, args json.RawMessage) (string, bool) {
			var in struct {
				Task string `json:"task"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return err.Error(), true
			}
			var want *uuid.UUID
			if strings.TrimSpace(in.Task) != "" {
				t, err := c.agentTaskByRef(ctx, a, in.Task)
				if err != nil {
					return err.Error(), true
				}
				want = &t.ID
			}
			task, err := c.store.ClaimTaskForAgent(ctx, a.ID, want, selfDrivenLease)
			if err != nil {
				return err.Error(), true
			}
			if task == nil {
				return "Nothing to claim: no matching task is waiting in TODO for you.", false
			}
			run, err := c.store.StartAgentRun(ctx, task.ID, a.ID)
			if err != nil {
				return err.Error(), true
			}
			// Tag the run now so an interrupted one is recognizable as self-driven.
			_, _ = c.store.Pool().Exec(ctx, `UPDATE agent_runs SET runtime='external', connector_id=$2 WHERE id=$1`, run.ID, a.RuntimeConnectorID)
			c.broker.Publish("task.updated", map[string]any{"id": task.ID, "status": task.Status})
			c.system(ctx, task.ID, fmt.Sprintf("%s claimed the task.", a.Name))
			team, _ := c.store.ListAgents(ctx, task.OrgID)
			var memories []store.AgentMemory
			if a.Has("memory") {
				memories, _ = c.store.Memories(ctx, a.ID, "", 30)
			}
			return selfDrivenPreamble(task) + buildSystemPrompt(a, task, team, memories) + "\n\n---\n\n" + c.buildBrief(ctx, task), false
		})

	add("get_task", "Re-read a task you have claimed: the brief with the latest thread.",
		obj(map[string]any{"task": taskProp}),
		func(ctx context.Context, args json.RawMessage) (string, bool) {
			task, err := c.ownedTask(ctx, a, args, false)
			if err != nil {
				return err.Error(), true
			}
			return fmt.Sprintf("Task #%d is %s.\n\n%s", task.Number, task.Status, c.buildBrief(ctx, task)), false
		})

	// The same tools built-in agents have, scoped to the claimed task.
	available := toolsFor(a)
	byName := map[string]tool{}
	for _, t := range available {
		byName[t.name] = t
	}
	for _, t := range available {
		if t.name == "get_task" {
			continue // replaced above: it reads the claimed task
		}
		schema := withTaskProperty(t.schema, taskProp)
		add(t.name, t.description, schema, func(ctx context.Context, args json.RawMessage) (string, bool) {
			task, err := c.ownedTask(ctx, a, args, true)
			if err != nil {
				return err.Error(), true
			}
			env := c.selfDrivenEnv(task, a)
			_, _ = c.store.ExtendTaskLease(ctx, task.ID, selfDrivenLease)
			out, isErr := c.execTool(guard.WithAgent(ctx), env, byName,
				llm.ToolCall{ID: uuid.NewString(), Name: t.name, Arguments: stripTaskArg(args)})
			if env.outcome != nil {
				c.finishSelfDriven(ctx, task, a, env)
			}
			return out, isErr
		})
	}
	return srv
}

func selfDrivenPreamble(t *store.Task) string {
	return fmt.Sprintf(`You are now working task #%d in CueFlow. Work only through the cueflow tools; pass "task": "%d" when you call them
if you have more than one task in progress. Drafts go to a human review queue and are never published by you.
Finish by calling complete_task (or ask_human if you need a decision). Your instructions and the task follow.

`, t.Number, t.Number)
}

// agentTaskByRef finds a task of the agent's org by number or id and checks it is assigned to the agent.
func (c *Crew) agentTaskByRef(ctx context.Context, a *store.Agent, ref string) (*store.Task, error) {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "#")
	var t *store.Task
	var err error
	if n, perr := strconv.ParseInt(ref, 10, 64); perr == nil {
		t, err = c.store.GetTaskByNumber(ctx, a.OrgID, n)
	} else if id, perr := uuid.Parse(ref); perr == nil {
		t, err = c.store.GetTask(ctx, id)
	} else {
		return nil, fmt.Errorf("%q is not a task number or id", ref)
	}
	if errors.Is(err, domain.ErrNotFound) || (err == nil && (t.OrgID != a.OrgID || t.AssigneeAgentID == nil || *t.AssigneeAgentID != a.ID)) {
		return nil, errors.New("no such task assigned to you")
	}
	return t, err
}

// ownedTask resolves the "task" argument (or the agent's only task in progress) and, when
// mustBeActive, requires the agent to hold the claim.
func (c *Crew) ownedTask(ctx context.Context, a *store.Agent, args json.RawMessage, mustBeActive bool) (*store.Task, error) {
	var in struct {
		Task string `json:"task"`
	}
	_ = json.Unmarshal(args, &in)
	var t *store.Task
	if strings.TrimSpace(in.Task) != "" {
		var err error
		if t, err = c.agentTaskByRef(ctx, a, in.Task); err != nil {
			return nil, err
		}
	} else {
		list, err := c.store.ListTasks(ctx, store.TaskFilter{OrgID: &a.OrgID, AssigneeID: &a.ID,
			Statuses: []store.TaskStatus{store.TaskInProgress}, Limit: 5})
		if err != nil {
			return nil, err
		}
		switch len(list) {
		case 0:
			return nil, errors.New("you have no task in progress: call claim_task first")
		case 1:
			t = list[0]
		default:
			return nil, errors.New("you have several tasks in progress: pass \"task\"")
		}
	}
	if mustBeActive && t.Status != store.TaskInProgress {
		return nil, fmt.Errorf("task #%d is %s, not in progress: claim it first, or it already finished", t.Number, t.Status)
	}
	return t, nil
}

func withTaskProperty(schema map[string]any, taskProp map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range schema {
		out[k] = v
	}
	props := map[string]any{}
	if old, ok := schema["properties"].(map[string]any); ok {
		for k, v := range old {
			props[k] = v
		}
	}
	props["task"] = taskProp
	out["properties"] = props
	return out
}

func stripTaskArg(args json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) != nil {
		return args
	}
	delete(m, "task")
	out, _ := json.Marshal(m)
	return out
}

// selfDrivenEnv keeps one runEnv per task so per-run limits (drafts, images, subtasks) hold across calls.
func (c *Crew) selfDrivenEnv(task *store.Task, a *store.Agent) *runEnv {
	if v, ok := c.selfEnvs.Load(task.ID); ok {
		return v.(*runEnv)
	}
	env := &runEnv{c: c, task: task, agent: a}
	c.selfEnvs.Store(task.ID, env)
	return env
}

// finishSelfDriven applies the outcome of a terminal tool call (complete_task, ask_human...).
func (c *Crew) finishSelfDriven(ctx context.Context, task *store.Task, a *store.Agent, env *runEnv) {
	bg := context.WithoutCancel(ctx)
	c.selfEnvs.Delete(task.ID)
	run, err := c.store.RunningAgentRun(bg, task.ID, a.ID)
	if err != nil {
		run = nil
	} else {
		run.Turns = max(run.Turns, 1)
	}
	c.finish(bg, task, a, run, env.outcome)
}

// recoverSelfDriven returns a self-driven task whose agent went quiet to TODO, so it can be claimed
// again. It reports whether the task was self-driven (and so handled here).
func (c *Crew) recoverSelfDriven(ctx context.Context, id uuid.UUID) bool {
	t, err := c.store.GetTask(ctx, id)
	if err != nil || t.AssigneeAgentID == nil {
		return false
	}
	a, err := c.store.GetAgent(ctx, *t.AssigneeAgentID)
	if err != nil || !c.isSelfDriven(ctx, a) {
		return false
	}
	c.selfEnvs.Delete(id)
	_ = c.store.FailExternalRuns(ctx, id, "the agent stopped responding (claim expired)")
	if ok, _ := c.store.SetTaskStatusIf(ctx, id, []store.TaskStatus{store.TaskInProgress}, store.TaskTodo, nil); ok {
		c.system(ctx, id, fmt.Sprintf("%s stopped responding; the task is back in To do.", a.Name))
		c.broker.Publish("task.updated", map[string]any{"id": id, "status": store.TaskTodo})
	}
	return true
}
