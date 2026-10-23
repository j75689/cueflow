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
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/config"
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
	// ContextHandOffAt is how full a run's context may get (prompt over the model's window) before it
	// hands off to a fresh run (CUEFLOW_CONTEXT_HANDOFF); the task page reads it from /meta.
	ContextHandOffAt float64
	Lease            time.Duration
	Every            time.Duration
	// DetachedWriteTimeout bounds each thread write (say, system, taskOrg) and the other writes a tool call
	// makes detached from its ctx (see detached); <= 0 means DefaultDetachedWriteTimeout.
	DetachedWriteTimeout time.Duration
	// ValidateSchedule checks cron expressions for agent-created schedules (set by main).
	ValidateSchedule store.CronValidator
	// OnSchedulesChanged reloads the scheduler after an agent creates/pauses a schedule.
	OnSchedulesChanged func(context.Context)
	// MCPBaseURL is how external agent processes on this host reach CueFlow's HTTP server
	// (e.g. http://127.0.0.1:3000); per-run tool servers live under MCPPathPrefix.
	MCPBaseURL string

	// PublicURL is how people reach the dashboard, for the task link in alerts (see notify.go).
	PublicURL string

	// Queue, when set (cloud deployment), replaces the in-process wake-up: Wake enqueues a
	// jobs.KindCrew delivery, handled by HandleCrew.
	Queue jobs.Queue

	mcp mcpRegistry
	// selfEnvs holds the per-task tool state of self-driven agents (see selfdriven.go).
	selfEnvs sync.Map
	// taskOrgs caches the org of tasks being worked for live-update events (a task never changes org); an
	// entry goes when the run ends (finish, and once more when runTask returns).
	taskOrgs sync.Map

	// DeliveryLimit is how long one queued delivery (an HTTP request from Cloud Tasks) may run before
	// Cloud Tasks' dispatch deadline and Cloud Run's request timeout, both 30 minutes, cut it off
	// (see delivery.go).
	DeliveryLimit time.Duration

	mu      sync.Mutex
	running map[uuid.UUID]*runSlot
	wake    chan struct{}
	wg      sync.WaitGroup
}

func New(st *store.Store, h *hub.Hub, b *events.Broker, logger *slog.Logger, allowPrivateFetch bool) *Crew {
	return &Crew{store: st, hub: h, broker: b, logger: logger.With("component", "crew"),
		fetcher: NewFetcher(allowPrivateFetch), Concurrency: 3, ContextHandOffAt: config.DefaultContextHandOffAt,
		Lease: 2 * time.Minute, Every: 2 * time.Second, DeliveryLimit: 30 * time.Minute,
		DetachedWriteTimeout: DefaultDetachedWriteTimeout, running: map[uuid.UUID]*runSlot{}, wake: make(chan struct{}, 1)}
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
	if slot, ok := c.running[taskID]; ok {
		slot.cancel(errStopped)
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
	claims, err := c.store.ClaimTodo(ctx, c.Lease, free)
	if err != nil {
		if ctx.Err() == nil {
			c.logger.Error("claim tasks", "err", err)
		}
		return
	}
	for _, cl := range claims {
		c.start(ctx, cl)
	}
}

func (c *Crew) start(ctx context.Context, cl store.Claim) {
	id := cl.ID
	runCtx, cancel := context.WithCancelCause(ctx)
	untrack := c.track(id, cancel)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			untrack()
			cancel(nil)
			c.Wake()
		}()
		c.runTask(runCtx, cl)
	}()
}

// HandleCrew is the jobs.KindCrew handler: it keeps watching the runs an earlier delivery handed
// over, claims TODO tasks (as many at once as this process has capacity for) and works them until
// their runs end or the delivery's time is nearly up (delivery.go), repeating until none are left.
// Every delivery is idempotent: claiming is atomic, so a duplicate finds nothing to do.
func (c *Crew) HandleCrew(ctx context.Context) error {
	start := time.Now()
	ctx = withDelivery(ctx, start, c.DeliveryLimit)
	for time.Now().Before(claimUntil(start, c.DeliveryLimit)) && ctx.Err() == nil {
		c.mu.Lock()
		free := max(c.Concurrency-len(c.running), 1)
		c.mu.Unlock()
		runs, err := c.store.ClaimHandedOverRuns(ctx, free)
		if err != nil {
			return err
		}
		// After the hand-overs are taken, so their leases are not mistaken for dead runs.
		c.recoverExpired(ctx)
		var claims []store.Claim
		if free -= len(runs); free > 0 {
			if claims, err = c.store.ClaimTodo(ctx, c.Lease, free); err != nil {
				return err
			}
		}
		if len(runs)+len(claims) == 0 {
			return nil
		}
		var wg sync.WaitGroup
		for _, id := range runs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c.resumeRun(ctx, id)
			}()
		}
		for _, cl := range claims {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c.runNow(ctx, cl)
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
func (c *Crew) runNow(ctx context.Context, cl store.Claim) {
	c.tracked(ctx, cl.ID, func(ctx context.Context) { c.runTask(ctx, cl) })
}

// tracked runs work for task id synchronously, where Cancel can stop it.
func (c *Crew) tracked(ctx context.Context, id uuid.UUID, work func(context.Context)) {
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	defer c.track(id, cancel)()
	work(runCtx)
}

// runSlot is a run's entry in c.running; its address tells the run's own entry from a newer one.
type runSlot struct{ cancel context.CancelCauseFunc }

// track records a run of task id as running here, where Cancel can stop it. The returned func
// removes the entry, unless a newer run of the same task (the task claimed again while this run
// was stalled) has replaced it.
func (c *Crew) track(id uuid.UUID, cancel context.CancelCauseFunc) func() {
	slot := &runSlot{cancel: cancel}
	c.mu.Lock()
	c.running[id] = slot
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		if c.running[id] == slot {
			delete(c.running, id)
		}
		c.mu.Unlock()
	}
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
		claims, err := c.store.ClaimTodo(ctx, c.Lease, 10)
		if err != nil || len(claims) == 0 {
			return
		}
		for _, cl := range claims {
			c.runTask(ctx, cl)
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
			if t, err := c.store.GetTask(ctx, id); err == nil {
				var a *store.Agent
				if t.AssigneeAgentID != nil {
					if got, err := c.store.GetAgent(ctx, *t.AssigneeAgentID); err == nil {
						a = got
					}
				}
				c.alert(ctx, t, a, "interrupted", "The run stopped (server restart or crash). Reply or press Run to continue.")
			}
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
	wctx, cancel := c.detached(ctx)
	defer cancel()
	m, err := c.store.AddTaskMessage(wctx, taskID, author, aid, kind, content, data)
	if err != nil {
		c.logger.Warn("thread write failed", "task", taskID, "err", err)
		return
	}
	c.broker.Publish(c.taskOrg(ctx, taskID), "task.message", map[string]any{"task_id": taskID, "id": m.ID})
}

func (c *Crew) system(ctx context.Context, taskID uuid.UUID, content string) {
	wctx, cancel := c.detached(ctx)
	defer cancel()
	m, err := c.store.AddTaskMessage(wctx, taskID, "system", nil, "status", content, nil)
	if err == nil {
		c.broker.Publish(c.taskOrg(ctx, taskID), "task.message", map[string]any{"task_id": taskID, "id": m.ID})
	}
}

// taskOrg is the org of a task, for the events about it; thread messages come often, so it is cached.
func (c *Crew) taskOrg(ctx context.Context, taskID uuid.UUID) uuid.UUID {
	if v, ok := c.taskOrgs.Load(taskID); ok {
		return v.(uuid.UUID)
	}
	wctx, cancel := c.detached(ctx)
	defer cancel()
	org := c.store.TaskOrg(wctx, taskID)
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

// runTask works the task of a claim, as the run the claim minted. Everything it does to the task's
// lease and status is fenced by that run id: a newer claim means this one was lost.
func (c *Crew) runTask(ctx context.Context, cl store.Claim) {
	id := cl.ID
	defer c.taskOrgs.Delete(id) // the run wrote most of the task's messages; don't keep the entry forever
	bg := context.WithoutCancel(ctx)
	task, err := c.store.GetTask(bg, id)
	if err != nil {
		c.logger.Error("load task", "task", id, "err", err)
		return
	}
	c.broker.Publish(task.OrgID, "task.updated", map[string]any{"id": id, "status": task.Status})
	if task.AssigneeAgentID == nil {
		c.finish(bg, task, nil, nil, &cl.RunID, &outcome{kind: "error", text: "task has no assignee"})
		return
	}
	a, err := c.store.GetAgent(bg, *task.AssigneeAgentID)
	if err != nil {
		c.finish(bg, task, nil, nil, &cl.RunID, &outcome{kind: "error", text: "assigned agent not found"})
		return
	}

	run, err := c.store.StartAgentRun(bg, cl.RunID, task.ID, a.ID)
	if err != nil {
		c.logger.Error("start run", "err", err)
		return
	}
	ctx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)
	ctx = withRunStop(ctx, cancelRun)
	stopBeat := c.heartbeat(ctx, cancelRun, task.ID, run.ID)
	defer stopBeat()

	var out *outcome
	if a.RuntimeConnectorID != nil {
		out = c.acpLoop(ctx, task, a, run)
	} else {
		out = c.loop(ctx, task, a, run)
	}
	c.endRun(ctx, task, a, run, out, stopBeat)
}

// endRun applies how a run ended: handed over to the next delivery, or finished. stopBeat ends the
// run's heartbeat first, so a last beat can't shorten the lease handOver sets.
func (c *Crew) endRun(ctx context.Context, task *store.Task, a *store.Agent, run *store.AgentRun, out *outcome, stopBeat func()) {
	bg := context.WithoutCancel(ctx)
	if out.kind == "handover" {
		stopBeat()
		if c.handOver(bg, task, run, out) {
			return
		}
		out = &outcome{kind: "canceled"} // stopped by a human, or the claim was lost (finish then records only the run)
	}
	if ctx.Err() != nil {
		out = stopped(ctx)
	}
	c.finish(bg, task, a, run, &run.ID, out)
}

// heartbeat keeps the task's lease for the claim's run. A lease it can't extend means the task
// left IN_PROGRESS (a human stopped or moved it, possibly through another instance) or was claimed
// again after this run stalled past its lease, so the run is stopped as by a human.
func (c *Crew) heartbeat(ctx context.Context, cancel context.CancelCauseFunc, id, runID uuid.UUID) func() {
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		t := time.NewTicker(c.Lease / 3)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if ok, err := c.store.ExtendTaskLease(context.WithoutCancel(ctx), id, runID, c.Lease); err == nil && !ok {
					cancel(errStopped)
					return
				}
			}
		}
	}()
	// Stopping waits for the goroutine, so no beat lands after it returns. Safe to call twice.
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-exited
	}
}

func (c *Crew) loop(ctx context.Context, task *store.Task, a *store.Agent, run *store.AgentRun) *outcome {
	model, connID, err := c.llmFor(ctx, a)
	if err != nil {
		return &outcome{kind: "error", text: err.Error()}
	}
	defer hub.Close(model)
	run.ConnectorID, run.Model, run.Runtime = &connID, model.Model(), "builtin"
	if w, ok := model.(llm.ContextWindower); ok {
		run.ContextWindow = w.ContextWindow(ctx)
	}

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
	env := &runEnv{c: c, task: task, agent: a, runID: &run.ID, stop: runStop(ctx)}
	defer func() {
		if env.searcher != nil {
			hub.Close(env.searcher)
		}
	}()

	msgs := []llm.Message{{Role: llm.RoleUser, Content: c.buildBrief(ctx, task)}}
	agentCtx := guard.WithAgent(ctx)
	var h handOff
	// A hand-off asked for on the last allowed turn still gets its turns to write the note.
	for run.Turns < a.MaxTurns || h.asked && h.turns < handOffTurns {
		if run.Turns > 0 && turnsOver(ctx) {
			return &outcome{kind: "continue"} // the delivery is nearly over; a fresh run takes the next turn
		}
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
		// The history is append-only, so this call's prompt is how full the context is now.
		run.ContextTokens = resp.Usage.InputTokens
		// A run that stalls most likely stalls in the model call: if the task was stopped, moved or
		// claimed again meanwhile, the turn it got back is stale, and none of it is written.
		if c.claimLost(ctx, env) {
			return env.outcome
		}
		c.recordContext(ctx, task, run)
		msgs = append(msgs, resp.Message)

		text := strings.TrimSpace(resp.Message.Content)
		if len(resp.Message.ToolCalls) == 0 && h.asked {
			// Asked for the note, it answered in text: that is the note. Either way the run hands off; it
			// must not end as complete with the note as its result.
			if text == "" {
				return c.handOffOutcome(ctx, task, a, run, false)
			}
			if _, err := c.store.WriteTaskFile(ctx, task.ID, handOffFile, "", []byte(text), a.Slug); err != nil {
				c.logger.Warn("save a hand-off note given as text", "task", task.ID, "err", err)
				c.say(ctx, task.ID, a, "message", text, map[string]any{"handoff": true})
				return c.handOffOutcome(ctx, task, a, run, false)
			}
			c.fileChanged(ctx, task.ID, handOffFile)
			return c.handOffOutcome(ctx, task, a, run, true)
		}
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
		// A new message at the end, never an edit of what was sent.
		if req, out := c.afterTurn(ctx, &h, env, run); out != nil {
			return out
		} else if req != nil {
			msgs = append(msgs, *req)
		}
	}
	return &outcome{kind: "limit"}
}

// recordContext stores the size of the run's latest prompt and tells the task's page.
func (c *Crew) recordContext(ctx context.Context, task *store.Task, run *store.AgentRun) {
	bg := context.WithoutCancel(ctx)
	if err := c.store.SetRunContext(bg, run.ID, run.ContextTokens, run.ContextWindow); err != nil {
		c.logger.Warn("record context size", "run", run.ID, "err", err)
		return
	}
	c.broker.Publish(task.OrgID, "run.context", map[string]any{"task_id": task.ID, "run_id": run.ID,
		"context_tokens": run.ContextTokens, "context_window": run.ContextWindow})
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

// loggedArgs shortens the arguments of tools whose input can be long before they go into the thread
// (stored, broadcast and replayed into every later brief). The tool itself gets them whole.
var loggedArgs = map[string]func(map[string]any) map[string]any{
	"write_file": func(m map[string]any) map[string]any {
		content, _ := m["content"].(string)
		return map[string]any{"name": m["name"], "characters": utf8.RuneCountInString(content), "content": clip(content, 2000)}
	},
}

// quietTools write their own thread entries; logging their call/result would be noise.
var quietTools = map[string]bool{"add_comment": true, "ask_human": true, "complete_task": true}

func (c *Crew) execTool(ctx context.Context, env *runEnv, byName map[string]tool, call llm.ToolCall) (string, bool) {
	// A run that lost its task's claim (it stalled past its lease and the task was claimed again)
	// must not act on the task before its heartbeat notices: it stops here, writing nothing. This is a
	// check before the call, not part of each write: what the tool does is not undone if the claim is
	// lost while it runs (the irreversible actions wait for a human anyway, as deliverables or tool
	// approvals). Not here: a self-driven agent's calls (runID nil), and RunApproval, which makes a
	// call a human approved rather than one a live run makes.
	if c.claimLost(ctx, env) {
		return "This run may no longer work on the task: it was stopped, moved, deleted or taken over by a newer run. Stop working.", true
	}
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
		if shorten := loggedArgs[call.Name]; shorten != nil {
			if m, ok := args.(map[string]any); ok {
				args = shorten(m)
			}
		}
		c.say(ctx, env.task.ID, env.agent, "tool_call", "", map[string]any{"tool": call.Name, "arguments": args})
	}
	res, err := t.run(ctx, env, call.Arguments)
	isErr := err != nil
	data := map[string]any{"tool": call.Name, "is_error": isErr}
	if isErr {
		res = "error: " + err.Error()
	} else if t.saveLarge {
		// Before the result reaches the model: what it sees is the stub, from the first time on.
		sctx, cancel := c.detached(ctx)
		if stub, name, ok := c.saveLargeResult(sctx, env.task.ID, env.agent.Slug, call.Name, call.Arguments, res); ok {
			res = stub
			data["file"] = name
		}
		cancel()
	}
	res = clipToolResult(res)
	if !quietTools[call.Name] {
		// say detaches the write: a call whose run was stopped still leaves its result in the thread.
		c.say(ctx, env.task.ID, env.agent, "tool_result", clip(res, 2000), data)
	}
	return res, isErr
}

// DefaultDetachedWriteTimeout is Crew.DetachedWriteTimeout's default.
const DefaultDetachedWriteTimeout = 30 * time.Second

// detached is ctx without its cancellation (a write a tool call makes must land even when its run is
// stopped or its request has ended), bounded by DetachedWriteTimeout instead, so a hung database or
// blob store can't hold the run's tool turn, and the request serving it, for good.
func (c *Crew) detached(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := c.DetachedWriteTimeout
	if timeout <= 0 {
		timeout = DefaultDetachedWriteTimeout
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// claimCheckTimeout bounds holdsClaim, so a hung database doesn't hold up every turn, tool call and
// runner request.
const claimCheckTimeout = 5 * time.Second

// holdsClaim is the claim check every fence before an action uses (claimLost, RunnerJobLost): whether
// runID may still act on the task (store.HoldsClaim). It fails open: a database error or a check that
// takes longer than claimCheckTimeout counts as held, and the run's heartbeat still fences it.
func (c *Crew) holdsClaim(ctx context.Context, taskID, runID uuid.UUID) bool {
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimCheckTimeout)
	defer cancel()
	held, err := c.store.HoldsClaim(checkCtx, taskID, runID)
	return err != nil || held
}

// claimLost reports whether the run may no longer act on its task (HoldsClaim), and then ends it:
// as a canceled outcome (on a run token it is recorded, so the instance that owns the run ends it
// too, and the runner's agent is stopped) and, where this instance owns the run, by canceling it at
// once. Once lost, it stays lost without asking again. A run with no runID is not fenced (see runEnv).
func (c *Crew) claimLost(ctx context.Context, env *runEnv) bool {
	if env.runID == nil {
		return false
	}
	if !env.lost {
		env.lost = !c.holdsClaim(ctx, env.task.ID, *env.runID)
	}
	if !env.lost {
		return false
	}
	if env.outcome == nil {
		env.outcome = &outcome{kind: "canceled"}
	}
	if env.stop != nil {
		env.stop(errStopped)
	}
	return true
}

// finish records the outcome and moves the task, unless a human already moved it elsewhere. It ends
// the claim of runID: if the task was claimed again since (this run stalled past its lease), the newer
// claim owns the task and its thread, so only the run is recorded. runID nil is a self-driven task's
// end, which is not fenced (see ExtendAgentTaskLease).
func (c *Crew) finish(ctx context.Context, task *store.Task, a *store.Agent, run *store.AgentRun, runID *uuid.UUID, out *outcome) {
	// Both run paths (runTask and a self-driven agent) end here, after writing most of the task's messages.
	defer c.taskOrgs.Delete(task.ID)
	if now, err := c.store.TaskRunID(ctx, task.ID); runID != nil && err == nil && (now == nil || *now != *runID) {
		if run != nil {
			run.Status, run.Outcome, run.Error = "CANCELED", "canceled", "the task was claimed again while this run was stalled"
			if err := c.store.FinishAgentRun(ctx, run); err != nil {
				c.logger.Warn("finish run", "err", err)
			}
		}
		return
	}
	next := store.TaskWaitingHuman
	var result *string
	runStatus := "SUCCEEDED"
	subtaskOfAgent := task.CreatedBy == "agent" && task.ParentID != nil
	alertText := out.text // what the alert says, when the task ends up waiting for a human

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
		alertText = fmt.Sprintf("The agent used its %d-turn budget. Reply to continue, or raise max turns.", a.MaxTurns)
		c.system(ctx, task.ID, alertText)
		runStatus = "FAILED"
		if run != nil {
			run.Error = "turn limit reached"
		}
	case "handoff":
		// The run ended to let a fresh one take over; the note (if any) is in the thread. A comment left
		// meanwhile re-queues the task like any other.
		auto, msg := c.autoHandOff(ctx, task, a, run, out)
		c.system(ctx, task.ID, msg)
		alertText = msg
		if auto {
			next = store.TaskTodo
			if run != nil {
				run.Outcome = outcomeAutoHandOff
			}
		}
	case "overflow":
		// Like "limit": the run used up what it had, and a comment left meanwhile still re-queues it.
		c.system(ctx, task.ID, "The agent run stopped: "+out.text)
		runStatus = "FAILED"
		if run != nil {
			run.Error = "context window exceeded"
		}
	case "continue":
		auto, msg := c.continueRun(ctx, task, run)
		c.system(ctx, task.ID, msg)
		alertText = msg
		if auto {
			next = store.TaskTodo
			if run != nil {
				run.Outcome = outcomeContinued
			}
		}
	case "canceled":
		c.system(ctx, task.ID, "Stopped.")
		runStatus = "CANCELED"
	case "interrupted":
		alertText = "The agent run was interrupted before it finished (the server stopped or its request was cut off). Reply or press Run to continue."
		c.system(ctx, task.ID, alertText)
		runStatus = "FAILED"
		if run != nil {
			run.Error = "interrupted"
		}
	default: // error
		c.system(ctx, task.ID, "The agent run failed: "+out.text)
		// The error can carry a request URL, request id or gateway body; keep those out of the alert.
		alertText = "The agent run failed. Open the task for details."
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
		if run.Outcome == "" {
			run.Outcome = out.kind
		}
		if err := c.store.FinishAgentRun(ctx, run); err != nil {
			c.logger.Warn("finish run", "err", err)
		}
	}

	var changed bool
	var err error
	if runID != nil {
		changed, err = c.store.SetClaimedTaskStatus(ctx, task.ID, *runID, next, result)
	} else {
		changed, err = c.store.SetTaskStatusIf(ctx, task.ID, []store.TaskStatus{store.TaskInProgress}, next, result)
	}
	if err != nil {
		c.logger.Error("finish task", "task", task.ID, "err", err)
		return
	}
	if !changed {
		return // a human moved the card meanwhile, or the task was claimed again
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
	if next == store.TaskWaitingHuman || next == store.TaskReview {
		c.alert(ctx, task, a, out.kind, alertText)
	}
}

// TaskChanged lets the API notify the crew after a human edit (e.g. a subtask dragged to Done).
func (c *Crew) TaskChanged(ctx context.Context, t *store.Task) {
	c.TaskChangedInBatch(ctx, t)
	if t.Status == store.TaskTodo {
		c.Wake()
	}
}

// TaskChangedInBatch is TaskChanged without the wake-up for a card now in TODO: a batch of edits
// wakes the crew once at its end.
func (c *Crew) TaskChangedInBatch(ctx context.Context, t *store.Task) {
	if t.ParentID != nil && t.Status.Closed() {
		c.resumeIfSubtasksDone(ctx, *t.ParentID)
	}
	if t.Status == store.TaskBlocked {
		c.resumeIfSubtasksDone(ctx, t.ID)
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
