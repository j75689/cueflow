// Package crew runs CueFlow's agent team. Tasks on the board that are assigned to an agent and in
// TODO are claimed (with a renewable lease), and the agent works them in a tool loop: researching,
// using the tools modules add (drafting, say), delegating subtasks to teammates, scheduling recurring
// work, asking the human, and finally reporting a result. Every step is written to the task thread.
package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/guard"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/jobs"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

type Crew struct {
	store   *store.Store
	hub     *hub.Hub
	broker  *events.Broker
	logger  *slog.Logger
	fetcher *Fetcher

	Concurrency int
	Lease       time.Duration
	Every       time.Duration
	// ValidateSchedule checks cron expressions for agent-created schedules (set by main).
	ValidateSchedule store.CronValidator
	// OnSchedulesChanged reloads the scheduler after an agent creates/pauses a schedule.
	OnSchedulesChanged func(context.Context)
	// MCPBaseURL is how external agent processes on this host reach CueFlow's HTTP server
	// (e.g. http://127.0.0.1:3000); per-run tool servers live under MCPPathPrefix.
	MCPBaseURL string

	// Queue, when set (cloud deployment), replaces the in-process wake-up: Wake enqueues a
	// jobs.KindCrew delivery, handled by HandleCrew.
	Queue jobs.Queue

	mcp mcpRegistry
	// selfEnvs holds the per-task tool state of self-driven agents (see selfdriven.go).
	selfEnvs sync.Map
	// taskOrgs caches the org of tasks being worked for live-update events (a task never changes org); an
	// entry goes when the run ends (finish, and once more when runTask returns).
	taskOrgs sync.Map

	mu      sync.Mutex
	running map[uuid.UUID]context.CancelFunc
	wake    chan struct{}
	wg      sync.WaitGroup
}

func New(st *store.Store, h *hub.Hub, b *events.Broker, logger *slog.Logger, allowPrivateFetch bool) *Crew {
	return &Crew{store: st, hub: h, broker: b, logger: logger.With("component", "crew"),
		fetcher: NewFetcher(allowPrivateFetch), Concurrency: 3, Lease: 2 * time.Minute, Every: 2 * time.Second,
		running: map[uuid.UUID]context.CancelFunc{}, wake: make(chan struct{}, 1)}
}

func (c *Crew) Wake() {
	if c.Queue != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := c.Queue.Enqueue(ctx, jobs.KindCrew, uuid.Nil, 0); err != nil {
			c.logger.Warn("enqueue crew work", "err", err)
		}
		return
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Cancel stops a running task's agent; it reports whether one was running in this process.
func (c *Crew) Cancel(taskID uuid.UUID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cancel, ok := c.running[taskID]; ok {
		cancel()
		return true
	}
	return false
}

func (c *Crew) Wait() { c.wg.Wait() }

func (c *Crew) Run(ctx context.Context) {
	t := time.NewTicker(c.Every)
	defer t.Stop()
	for {
		c.dispatch(ctx)
		select {
		case <-ctx.Done():
			c.wg.Wait()
			return
		case <-t.C:
		case <-c.wake:
		}
	}
}

func (c *Crew) dispatch(ctx context.Context) {
	c.recoverExpired(ctx)
	c.mu.Lock()
	free := c.Concurrency - len(c.running)
	c.mu.Unlock()
	if free <= 0 {
		return
	}
	ids, err := c.store.ClaimTodo(ctx, c.Lease, free)
	if err != nil {
		if ctx.Err() == nil {
			c.logger.Error("claim tasks", "err", err)
		}
		return
	}
	for _, id := range ids {
		c.start(ctx, id)
	}
}

func (c *Crew) start(ctx context.Context, id uuid.UUID) {
	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.running[id] = cancel
	c.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			c.mu.Lock()
			delete(c.running, id)
			c.mu.Unlock()
			cancel()
			c.Wake()
		}()
		c.runTask(runCtx, id)
	}()
}

// handlerBudget bounds one crew delivery; whatever is left is picked up by a fresh one.
const handlerBudget = 25 * time.Minute

// HandleCrew is the jobs.KindCrew handler: it claims TODO tasks (as many at once as this process
// has capacity for) and works them to the end of their runs, repeating until none are left. Every
// delivery is idempotent: claiming is atomic, so a duplicate finds nothing to do.
func (c *Crew) HandleCrew(ctx context.Context) error {
	deadline := time.Now().Add(handlerBudget)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		c.recoverExpired(ctx)
		c.mu.Lock()
		free := max(c.Concurrency-len(c.running), 1)
		c.mu.Unlock()
		ids, err := c.store.ClaimTodo(ctx, c.Lease, free)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		var wg sync.WaitGroup
		for _, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c.runNow(ctx, id)
			}()
		}
		wg.Wait()
	}
	if n, _ := c.store.TodoTaskCount(context.WithoutCancel(ctx)); n > 0 {
		c.Wake()
	}
	return nil
}

// runNow works one claimed task synchronously.
func (c *Crew) runNow(ctx context.Context, id uuid.UUID) {
	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.running[id] = cancel
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.running, id)
		c.mu.Unlock()
		cancel()
	}()
	c.runTask(runCtx, id)
}

// Sweep is the crew's part of the external clock: it returns interrupted tasks to the board and
// makes sure waiting work has a delivery coming.
func (c *Crew) Sweep(ctx context.Context) error {
	c.recoverExpired(ctx)
	if err := c.store.FailAbandoned(ctx); err != nil {
		c.logger.Warn("close abandoned work", "err", err)
	}
	n, err := c.store.TodoTaskCount(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		c.Wake()
	}
	return nil
}

// RunPending claims and works TODO tasks synchronously until none remain or maxRounds is hit.
// Intended for tests and one-shot CLI use.
func (c *Crew) RunPending(ctx context.Context, maxRounds int) {
	for i := 0; i < maxRounds; i++ {
		ids, err := c.store.ClaimTodo(ctx, c.Lease, 10)
		if err != nil || len(ids) == 0 {
			return
		}
		for _, id := range ids {
			c.runTask(ctx, id)
		}
	}
}

func (c *Crew) recoverExpired(ctx context.Context) {
	ids, err := c.store.ExpiredTaskLeases(ctx)
	if err != nil {
		return
	}
	for _, id := range ids {
		c.mu.Lock()
		_, mine := c.running[id]
		c.mu.Unlock()
		if mine {
			continue
		}
		if c.recoverSelfDriven(ctx, id) {
			continue
		}
		if ok, _ := c.store.SetTaskStatusIf(ctx, id, []store.TaskStatus{store.TaskInProgress}, store.TaskWaitingHuman, nil); ok {
			c.system(ctx, id, "The agent run was interrupted (server restart or crash). Reply or press Run to continue.")
			c.broker.Publish(c.taskOrg(ctx, id), "task.updated", map[string]any{"id": id, "status": store.TaskWaitingHuman})
		}
	}
}

// --- thread helpers ---

func (c *Crew) say(ctx context.Context, taskID uuid.UUID, a *store.Agent, kind, content string, data any) {
	var aid *uuid.UUID
	if a != nil {
		aid = &a.ID
	}
	author := "agent"
	if kind == "tool_result" {
		author = "tool"
	}
	m, err := c.store.AddTaskMessage(context.WithoutCancel(ctx), taskID, author, aid, kind, content, data)
	if err != nil {
		c.logger.Warn("thread write failed", "task", taskID, "err", err)
		return
	}
	c.broker.Publish(c.taskOrg(ctx, taskID), "task.message", map[string]any{"task_id": taskID, "id": m.ID})
}

func (c *Crew) system(ctx context.Context, taskID uuid.UUID, content string) {
	m, err := c.store.AddTaskMessage(context.WithoutCancel(ctx), taskID, "system", nil, "status", content, nil)
	if err == nil {
		c.broker.Publish(c.taskOrg(ctx, taskID), "task.message", map[string]any{"task_id": taskID, "id": m.ID})
	}
}

// taskOrg is the org of a task, for the events about it; thread messages come often, so it is cached.
func (c *Crew) taskOrg(ctx context.Context, taskID uuid.UUID) uuid.UUID {
	if v, ok := c.taskOrgs.Load(taskID); ok {
		return v.(uuid.UUID)
	}
	org := c.store.TaskOrg(context.WithoutCancel(ctx), taskID)
	if org != uuid.Nil {
		c.taskOrgs.Store(taskID, org)
	}
	return org
}

func (c *Crew) schedulesChanged(ctx context.Context) {
	if c.OnSchedulesChanged != nil {
		c.OnSchedulesChanged(ctx)
	}
}

func (c *Crew) llmFor(ctx context.Context, a *store.Agent) (connector.LLM, uuid.UUID, error) {
	id := a.LLMConnectorID
	if id == nil {
		conn, err := c.store.FirstActiveConnector(ctx, connector.KindLLM)
		if err != nil {
			return nil, uuid.Nil, errors.New("no LLM connector configured (add one under Connectors)")
		}
		id = &conn.ID
	}
	m, _, err := c.hub.LLM(ctx, *id)
	return m, *id, err
}

// --- the agent loop ---

func (c *Crew) runTask(ctx context.Context, id uuid.UUID) {
	defer c.taskOrgs.Delete(id) // the run wrote most of the task's messages; don't keep the entry forever
	bg := context.WithoutCancel(ctx)
	task, err := c.store.GetTask(bg, id)
	if err != nil {
		c.logger.Error("load task", "task", id, "err", err)
		return
	}
	c.broker.Publish(task.OrgID, "task.updated", map[string]any{"id": id, "status": task.Status})
	if task.AssigneeAgentID == nil {
		c.finish(bg, task, nil, nil, &outcome{kind: "error", text: "task has no assignee"})
		return
	}
	a, err := c.store.GetAgent(bg, *task.AssigneeAgentID)
	if err != nil {
		c.finish(bg, task, nil, nil, &outcome{kind: "error", text: "assigned agent not found"})
		return
	}

	run, err := c.store.StartAgentRun(bg, task.ID, a.ID)
	if err != nil {
		c.logger.Error("start run", "err", err)
		return
	}
	ctx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	stopBeat := c.heartbeat(ctx, cancelRun, task.ID)
	defer stopBeat()

	var out *outcome
	if a.RuntimeConnectorID != nil {
		out = c.acpLoop(ctx, task, a, run)
	} else {
		out = c.loop(ctx, task, a, run)
	}
	if ctx.Err() != nil {
		out = &outcome{kind: "canceled"}
	}
	c.finish(bg, task, a, run, out)
}

// heartbeat keeps a running task's lease alive. When the task is no longer IN_PROGRESS (a human
// canceled it, possibly through another instance) it calls cancel so the run stops.
func (c *Crew) heartbeat(ctx context.Context, cancel context.CancelFunc, id uuid.UUID) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(c.Lease / 3)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if ok, err := c.store.ExtendTaskLease(context.WithoutCancel(ctx), id, c.Lease); err == nil && !ok {
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

func (c *Crew) loop(ctx context.Context, task *store.Task, a *store.Agent, run *store.AgentRun) *outcome {
	model, connID, err := c.llmFor(ctx, a)
	if err != nil {
		return &outcome{kind: "error", text: err.Error()}
	}
	defer hub.Close(model)
	run.ConnectorID, run.Model, run.Runtime = &connID, model.Model(), "builtin"

	team, _ := c.store.ListAgents(ctx, task.OrgID)
	var memories []store.AgentMemory
	if a.Has("memory") {
		memories, _ = c.store.Memories(ctx, a.ID, "", 30)
	}
	system := buildSystemPrompt(a, task, team, memories)
	available := c.agentTools(ctx, a)
	byName := map[string]tool{}
	llmTools := make([]llm.Tool, 0, len(available))
	for _, t := range available {
		byName[t.name] = t
		llmTools = append(llmTools, llm.Tool{Name: t.name, Description: t.description, Schema: t.schema})
	}
	env := &runEnv{c: c, task: task, agent: a}
	defer func() {
		if env.searcher != nil {
			hub.Close(env.searcher)
		}
	}()

	msgs := []llm.Message{{Role: llm.RoleUser, Content: c.buildBrief(ctx, task)}}
	agentCtx := guard.WithAgent(ctx)
	for run.Turns < a.MaxTurns {
		resp, err := model.Chat(agentCtx, llm.Request{System: system, Messages: msgs, Tools: llmTools})
		if err != nil {
			if ctx.Err() != nil {
				return &outcome{kind: "canceled"}
			}
			if errors.Is(err, llm.ErrContextOverflow) {
				c.logger.Warn("context window exceeded", "task", task.ID, "turns", run.Turns, "err", err)
				return &outcome{kind: "overflow", text: overflowMessage(run.Turns)}
			}
			return &outcome{kind: "error", text: err.Error()}
		}
		run.Turns++
		run.InputTokens += resp.Usage.InputTokens
		run.OutputTokens += resp.Usage.OutputTokens
		run.CacheReadTokens += resp.Usage.CacheReadTokens
		run.CacheWriteTokens += resp.Usage.CacheWriteTokens
		msgs = append(msgs, resp.Message)

		text := strings.TrimSpace(resp.Message.Content)
		if len(resp.Message.ToolCalls) == 0 {
			if text == "" {
				if resp.StopReason == "max_tokens" {
					return &outcome{kind: "error", text: "the model hit its output limit"}
				}
				text = "(finished without a summary)"
			}
			return &outcome{kind: "complete", text: text}
		}
		if text != "" {
			c.say(ctx, task.ID, a, "message", text, nil)
		}
		for _, call := range resp.Message.ToolCalls {
			run.ToolCalls++
			if env.outcome != nil && env.outcome.kind == "approval" {
				// The run is pausing for a human decision; don't act on later calls in this turn.
				msgs = append(msgs, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, IsError: true,
					Content: "Not run: this task is waiting for the human to approve an earlier call."})
				continue
			}
			result, isErr := c.execTool(agentCtx, env, byName, call)
			msgs = append(msgs, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Content: result, IsError: isErr})
		}
		if env.outcome != nil {
			return env.outcome
		}
		if ctx.Err() != nil {
			return &outcome{kind: "canceled"}
		}
	}
	return &outcome{kind: "limit"}
}

// overflowMessage explains a refusal for exceeding the model's context window. The provider's raw
// error goes to the log. On the first turn nothing the run did is to blame (the agent's instructions,
// memories, tool schemas and the brief are already too big, or max_tokens doesn't fit the window),
// and a reply would only fail the same way.
func overflowMessage(turns int) string {
	if turns == 0 {
		return "the first request doesn't fit the model's context window. It carries the agent's instructions, its memories, " +
			"the schemas of its tools and the task brief (description and thread). Trim the largest of those (fewer tools or " +
			"memories, a shorter description), use a model with a larger window, or lower the connector's max output tokens."
	}
	// The next run starts from the brief and the thread, not this run's history. Many providers count
	// max_tokens against the window too, so a small window can run out early.
	return fmt.Sprintf("after %d turns the conversation no longer fits the model's context window. "+
		"Reply to continue in a fresh run (it starts from the task and its thread), or split the work into subtasks. "+
		"If this happens early, lower the connector's max output tokens or use a model with a larger window.", turns)
}

// quietTools write their own thread entries; logging their call/result would be noise.
var quietTools = map[string]bool{"add_comment": true, "ask_human": true, "complete_task": true}

func (c *Crew) execTool(ctx context.Context, env *runEnv, byName map[string]tool, call llm.ToolCall) (string, bool) {
	t, ok := byName[call.Name]
	if !ok {
		// Models often call an MCP tool by its bare name ("render_reel" for "reels_renderer__render_reel").
		var matches []string
		for name := range byName {
			if strings.HasSuffix(name, "__"+call.Name) {
				matches = append(matches, name)
			}
		}
		if len(matches) == 1 {
			t, ok = byName[matches[0]], true
			call.Name = matches[0]
		}
	}
	if !ok {
		c.logger.Warn("agent requested a tool it does not have", "agent", env.agent.Slug, "tool", call.Name)
		msg := fmt.Sprintf("Tool %q is not available to you.", call.Name)
		var similar []string
		for name := range byName {
			if strings.Contains(name, call.Name) || strings.Contains(call.Name, strings.TrimPrefix(name[strings.LastIndex(name, "__")+1:], "_")) {
				similar = append(similar, name)
			}
		}
		if len(similar) > 0 {
			sort.Strings(similar)
			msg += " Did you mean: " + strings.Join(similar, ", ") + "?"
		} else if strings.Contains(call.Name, "publish") {
			msg += " Publishing is always done by humans after review."
		}
		c.say(ctx, env.task.ID, env.agent, "tool_result", msg, map[string]any{"tool": call.Name, "is_error": true})
		return msg, true
	}
	if !quietTools[call.Name] {
		var args any = map[string]any{}
		_ = json.Unmarshal(call.Arguments, &args)
		c.say(ctx, env.task.ID, env.agent, "tool_call", "", map[string]any{"tool": call.Name, "arguments": args})
	}
	res, err := t.run(ctx, env, call.Arguments)
	isErr := err != nil
	if isErr {
		res = "error: " + err.Error()
	}
	res = clipToolResult(res)
	if !quietTools[call.Name] {
		c.say(ctx, env.task.ID, env.agent, "tool_result", clip(res, 2000), map[string]any{"tool": call.Name, "is_error": isErr})
	}
	return res, isErr
}

// finish records the outcome and moves the task, unless a human already moved it elsewhere.
func (c *Crew) finish(ctx context.Context, task *store.Task, a *store.Agent, run *store.AgentRun, out *outcome) {
	// Both run paths (runTask and a self-driven agent) end here, after writing most of the task's messages.
	defer c.taskOrgs.Delete(task.ID)
	next := store.TaskWaitingHuman
	var result *string
	runStatus := "SUCCEEDED"
	subtaskOfAgent := task.CreatedBy == "agent" && task.ParentID != nil

	switch out.kind {
	case "complete":
		c.say(ctx, task.ID, a, "result", out.text, nil)
		result = &out.text
		next = store.TaskReview
		if subtaskOfAgent {
			next = store.TaskDone
		}
	case "ask":
		c.say(ctx, task.ID, a, "question", out.text, nil)
	case "approval":
		// The approval request is already in the thread; wait for the human's decision.
	case "wait":
		c.system(ctx, task.ID, out.text)
		next = store.TaskBlocked
	case "limit":
		c.system(ctx, task.ID, fmt.Sprintf("The agent used its %d-turn budget. Reply to continue, or raise max turns.", a.MaxTurns))
		runStatus = "FAILED"
		if run != nil {
			run.Error = "turn limit reached"
		}
	case "overflow":
		// Like "limit": the run used up what it had, and a comment left meanwhile still re-queues it.
		c.system(ctx, task.ID, "The agent run stopped: "+out.text)
		runStatus = "FAILED"
		if run != nil {
			run.Error = "context window exceeded"
		}
	case "canceled":
		c.system(ctx, task.ID, "Stopped.")
		runStatus = "CANCELED"
	default: // error
		c.system(ctx, task.ID, "The agent run failed: "+out.text)
		runStatus = "FAILED"
		if run != nil {
			run.Error = out.text
		}
	}

	// A human commented while the agent was working: run again so the comment is addressed.
	// Not after an overflow on the first turn: the new run would build the same first request.
	rerunnable := out.kind != "canceled" && out.kind != "error" && !(out.kind == "overflow" && run != nil && run.Turns == 0)
	if run != nil && rerunnable && next != store.TaskBlocked {
		if newer, _ := c.store.HumanMessageSince(ctx, task.ID, run.StartedAt); newer {
			next = store.TaskTodo
		}
	}
	if run != nil {
		run.Status = runStatus
		if err := c.store.FinishAgentRun(ctx, run); err != nil {
			c.logger.Warn("finish run", "err", err)
		}
	}

	changed, err := c.store.SetTaskStatusIf(ctx, task.ID, []store.TaskStatus{store.TaskInProgress}, next, result)
	if err != nil {
		c.logger.Error("finish task", "task", task.ID, "err", err)
		return
	}
	if !changed {
		return // a human moved the card meanwhile
	}
	c.broker.Publish(task.OrgID, "task.updated", map[string]any{"id": task.ID, "status": next})
	if next == store.TaskBlocked {
		// Subtasks may all have finished while we were still running.
		c.resumeIfSubtasksDone(ctx, task.ID)
	}
	if next == store.TaskTodo {
		c.Wake()
	}
	if task.ParentID != nil && (next == store.TaskDone || next == store.TaskReview) {
		c.resumeIfSubtasksDone(ctx, *task.ParentID)
	}
}

// TaskChanged lets the API notify the crew after a human edit (e.g. a subtask dragged to Done).
func (c *Crew) TaskChanged(ctx context.Context, t *store.Task) {
	if t.ParentID != nil && t.Status.Closed() {
		c.resumeIfSubtasksDone(ctx, *t.ParentID)
	}
	if t.Status == store.TaskBlocked {
		c.resumeIfSubtasksDone(ctx, t.ID)
	}
	if t.Status == store.TaskTodo {
		c.Wake()
	}
}

// resumeIfSubtasksDone moves a BLOCKED parent back to TODO once every subtask is finished,
// posting their results to the parent's thread.
func (c *Crew) resumeIfSubtasksDone(ctx context.Context, parentID uuid.UUID) {
	parent, err := c.store.GetTask(ctx, parentID)
	if err != nil || parent.Status != store.TaskBlocked {
		return
	}
	open, err := c.store.OpenSubtasks(ctx, parentID)
	if err != nil || open > 0 {
		return
	}
	subs, _ := c.store.ListTasks(ctx, store.TaskFilter{ParentID: &parentID, All: true})
	var b strings.Builder
	b.WriteString("All subtasks finished:")
	for _, s := range subs {
		fmt.Fprintf(&b, "\n- %s", taskLine(s))
		if s.Result != "" {
			fmt.Fprintf(&b, ": %s", clip(s.Result, 300))
		}
	}
	if ok, _ := c.store.SetTaskStatusIf(ctx, parentID, []store.TaskStatus{store.TaskBlocked}, store.TaskTodo, nil); ok {
		c.system(ctx, parentID, b.String())
		c.broker.Publish(c.taskOrg(ctx, parentID), "task.updated", map[string]any{"id": parentID, "status": store.TaskTodo})
		c.Wake()
	}
}
