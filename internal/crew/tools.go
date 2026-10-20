package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/store"
)

type tool struct {
	name        string
	description string
	schema      map[string]any
	capability  string // "" = every agent
	run         func(ctx context.Context, e *runEnv, args json.RawMessage) (string, error)
}

// outcome is set by a terminal tool and ends the run.
type outcome struct {
	kind string // complete | ask | wait | limit | error
	text string
}

type runEnv struct {
	c        *Crew
	task     *store.Task
	agent    *store.Agent
	outcome  *outcome
	subtasks int
	counts   map[string]int // per-run counters of module tools (see Run.Inc)

	searcher    connector.Searcher
	searcherErr error
	searchTried bool
}

const (
	maxSubtasksPerRun = 8
	maxDelegateDepth  = 3
	// maxToolResult is how much of one tool result the model sees: enough for a source file of a
	// few hundred lines.
	maxToolResult = 32000
)

// clipToolResult cuts a result to maxToolResult and says how much was left out, so the agent knows
// it has not seen the whole thing.
func clipToolResult(s string) string { return clipNoted(s, maxToolResult) }

// clipNoted cuts s to n characters and, when it does, ends it with how many were shown out of how
// many.
func clipNoted(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return fmt.Sprintf("%s… (truncated: showed the first %d of %d characters)", string(r[:n]), n, len(r))
}

// run is the module-facing view of this run.
func (e *runEnv) run() *Run {
	return &Run{Deps: Deps{Store: e.c.store, Hub: e.c.hub, Broker: e.c.broker, Logger: e.c.logger}, Task: e.task, Agent: e.agent, env: e}
}

// Obj, Str, StrList and Integer build JSON schemas for tool arguments.
func Obj(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func Str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func StrList(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}
func Integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

var priorityEnum = map[string]any{"type": "string", "enum": []string{"low", "normal", "high", "urgent"}}

// coreCapabilities are the ones every install has; modules add theirs with RegisterCapability.
var coreCapabilities = []Capability{
	{ID: "research", Name: "Research", Description: "Search the web and read pages."},
	{ID: "delegation", Name: "Delegation", Description: "Split work into subtasks for teammates and wait for their results."},
	{ID: "planning", Name: "Planning", Description: "Look at and create tasks on the board, and set up recurring schedules."},
	{ID: "memory", Name: "Memory", Description: "Remember facts and preferences across tasks."},
}

var coreTools []tool

func init() {
	coreTools = []tool{
		// --- always available ---
		{name: "add_comment", description: "Post a short progress update to the task thread (visible to the human).",
			schema: Obj(map[string]any{"content": Str("Update text")}, "content"), run: toolAddComment},
		{name: "ask_human", description: "Ask the human a question and pause until they reply. Use only when you genuinely need their decision or missing information.",
			schema: Obj(map[string]any{"question": Str("The question, with options if helpful")}, "question"), run: toolAskHuman},
		{name: "complete_task", description: "Finish the task with a concise summary of what you did and the outputs (subtask numbers, links, the ids of anything you created).",
			schema: Obj(map[string]any{"summary": Str("Result summary for the human")}, "summary"), run: toolComplete},

		// --- research ---
		{name: "web_search", capability: "research", description: "Search the web for recent information.",
			schema: Obj(map[string]any{"query": Str("Search query"), "limit": Integer("Max results (default 5)")}, "query"), run: toolWebSearch},
		{name: "read_url", capability: "research", description: "Read the text of a public web page.",
			schema: Obj(map[string]any{"url": Str("Absolute http(s) URL")}, "url"), run: toolReadURL},
		// --- delegation ---
		{name: "list_agents", capability: "delegation", description: "List teammates you can delegate to.",
			schema: Obj(map[string]any{}), run: toolListAgents},
		{name: "create_subtask", capability: "delegation", description: "Delegate part of this task to a teammate. They start immediately.",
			schema: Obj(map[string]any{
				"title": Str("Clear, specific title"), "description": Str("Everything they need: goal, context, expected output"),
				"assignee": Str("Teammate slug, e.g. writer"), "priority": priorityEnum,
			}, "title", "description", "assignee"), run: toolCreateSubtask},
		{name: "wait_for_subtasks", capability: "delegation", description: "Pause until all your subtasks finish; you will be resumed with their results.",
			schema: Obj(map[string]any{}), run: toolWaitSubtasks},

		// --- planning ---
		{name: "list_tasks", capability: "planning", description: "List tasks on the board.",
			schema: Obj(map[string]any{"status": Str("Optional status filter"), "assignee": Str("Optional agent slug"), "limit": Integer("Max tasks (default 20)")}), run: toolListTasks},
		{name: "get_task", capability: "planning", description: "Read a task by number, including its result and subtasks.",
			schema: Obj(map[string]any{"number": Integer("Task number")}, "number"), run: toolGetTask},
		{name: "create_task", capability: "planning", description: "Create a separate task on the board (not a subtask). Assign it to start it.",
			schema: Obj(map[string]any{
				"title": Str("Title"), "description": Str("Details"), "assignee": Str("Optional agent slug"),
				"priority": priorityEnum,
			}, "title"), run: toolCreateTask},
		{name: "schedule_task", capability: "planning", description: "Create a recurring schedule that opens a task for an agent on a cron expression (minute hour day month weekday), at most every 5 minutes.",
			schema: Obj(map[string]any{
				"name": Str("Schedule name"), "cron": Str("Cron, e.g. '0 8 * * *' for 08:00 daily"),
				"timezone": Str("IANA timezone, e.g. Asia/Taipei"), "title": Str("Title of each task"),
				"description": Str("Instructions for each run"), "assignee": Str("Agent slug (default: yourself)"),
				"priority": priorityEnum,
			}, "cron", "title", "description"), run: toolScheduleTask},
		{name: "list_schedules", capability: "planning", description: "List recurring schedules.",
			schema: Obj(map[string]any{}), run: toolListSchedules},
		{name: "pause_schedule", capability: "planning", description: "Pause a recurring schedule by ID.",
			schema: Obj(map[string]any{"id": Str("Schedule ID")}, "id"), run: toolPauseSchedule},

		// --- memory ---
		{name: "remember", capability: "memory", description: "Save a durable note (a preference, fact, or decision) for future tasks.",
			schema: Obj(map[string]any{"content": Str("The note")}, "content"), run: toolRemember},
		{name: "search_memory", capability: "memory", description: "Search your saved notes.",
			schema: Obj(map[string]any{"query": Str("Keyword")}, "query"), run: toolSearchMemory},
	}
	coreTools = append(coreTools, fileTools...) // always available, like the tools above
	store.ValidCapability = validCapability
}

// toolCatalog is every built-in tool: the core's and the modules'.
func toolCatalog() []tool {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return allTools()
}

func toolsFor(a *store.Agent) []tool {
	var out []tool
	for _, t := range toolCatalog() {
		if t.capability == "" || a.Has(t.capability) {
			out = append(out, t)
		}
	}
	return out
}

// DecodeArgs parses a tool call's arguments; a missing body is an empty object.
func DecodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

// JSONResult renders a tool result as JSON.
func JSONResult(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// --- base tools ---

func toolAddComment(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ Content string }
	if err := DecodeArgs(raw, &a); err != nil || strings.TrimSpace(a.Content) == "" {
		return "", errors.New("content is required")
	}
	e.c.say(ctx, e.task.ID, e.agent, "message", a.Content, nil)
	return "posted", nil
}

func toolAskHuman(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ Question string }
	if err := DecodeArgs(raw, &a); err != nil || strings.TrimSpace(a.Question) == "" {
		return "", errors.New("question is required")
	}
	e.outcome = &outcome{kind: "ask", text: a.Question}
	return "Question posted. Stop now; you will be resumed when the human replies.", nil
}

func toolComplete(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ Summary string }
	if err := DecodeArgs(raw, &a); err != nil || strings.TrimSpace(a.Summary) == "" {
		return "", errors.New("summary is required")
	}
	e.outcome = &outcome{kind: "complete", text: a.Summary}
	return "Task completed.", nil
}

// --- research ---

func (e *runEnv) search(ctx context.Context) (connector.Searcher, error) {
	if e.searchTried {
		return e.searcher, e.searcherErr
	}
	e.searchTried = true
	id := e.agent.SearchConnectorID
	if id == nil {
		c, err := e.c.store.FirstActiveConnector(ctx, connector.KindSearch)
		if err != nil {
			e.searcherErr = errors.New("no search connector configured (add SearXNG or Tavily under Connectors)")
			return nil, e.searcherErr
		}
		id = &c.ID
	}
	e.searcher, _, e.searcherErr = e.c.hub.Searcher(ctx, *id)
	return e.searcher, e.searcherErr
}

func toolWebSearch(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Query string
		Limit int
	}
	if err := DecodeArgs(raw, &a); err != nil || strings.TrimSpace(a.Query) == "" {
		return "", errors.New("query is required")
	}
	if a.Limit <= 0 || a.Limit > 10 {
		a.Limit = 5
	}
	s, err := e.search(ctx)
	if err != nil {
		return "", err
	}
	res, err := s.Search(ctx, a.Query, a.Limit)
	if err != nil {
		return "", err
	}
	return JSONResult(res)
}

func toolReadURL(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ URL string }
	if err := DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	page, err := e.c.fetcher.Fetch(ctx, a.URL)
	if err != nil {
		return "", err
	}
	return JSONResult(page)
}

// --- delegation ---

func toolListAgents(ctx context.Context, e *runEnv, _ json.RawMessage) (string, error) {
	agents, err := e.c.store.ListAgents(ctx, e.task.OrgID)
	if err != nil {
		return "", err
	}
	type row struct {
		Slug         string   `json:"slug"`
		Name         string   `json:"name"`
		Role         string   `json:"role"`
		Description  string   `json:"description"`
		Capabilities []string `json:"capabilities"`
		OpenTasks    int      `json:"open_tasks"`
	}
	out := []row{}
	for _, a := range agents {
		if a.IsActive {
			out = append(out, row{a.Slug, a.Name, a.Role, a.Description, a.Capabilities, a.Stats.OpenTasks})
		}
	}
	return JSONResult(out)
}

func (e *runEnv) resolveAgent(ctx context.Context, slug string) (*store.Agent, error) {
	if strings.TrimSpace(slug) == "" {
		return e.agent, nil
	}
	a, err := e.c.store.GetAgentBySlug(ctx, e.task.OrgID, slug)
	if err != nil {
		return nil, fmt.Errorf("no teammate with slug %q (use list_agents)", slug)
	}
	if !a.IsActive {
		return nil, fmt.Errorf("@%s is disabled", a.Slug)
	}
	return a, nil
}

func toolCreateSubtask(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Title, Description, Assignee, Priority string
	}
	if err := DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	if e.subtasks >= maxSubtasksPerRun {
		return "", fmt.Errorf("subtask limit reached (%d per run)", maxSubtasksPerRun)
	}
	depth, err := e.c.store.TaskDepth(ctx, e.task.ID)
	if err != nil {
		return "", err
	}
	if depth+1 >= maxDelegateDepth {
		return "", errors.New("delegation is nested too deeply; do this part yourself")
	}
	assignee, err := e.resolveAgent(ctx, a.Assignee)
	if err != nil {
		return "", err
	}
	parent := e.task.ID
	creator := e.agent.ID
	t, err := e.c.store.CreateTask(ctx, store.NewTask{
		Title: a.Title, Description: a.Description, Priority: a.Priority, Status: store.TaskTodo,
		AssigneeAgentID: &assignee.ID, CreatedBy: "agent", CreatorAgentID: &creator, ParentID: &parent,
		PublisherConnectorID: e.task.PublisherConnectorID, PublishAccounts: e.task.PublishAccounts,
	})
	if err != nil {
		return "", err
	}
	e.subtasks++
	e.c.system(ctx, t.ID, fmt.Sprintf("Delegated by @%s from task #%d.", e.agent.Slug, e.task.Number))
	e.c.broker.Publish(t.OrgID, "task.updated", map[string]any{"id": t.ID, "status": t.Status})
	e.c.Wake()
	return fmt.Sprintf("Subtask #%d created and assigned to @%s. Call wait_for_subtasks when you have delegated everything.", t.Number, assignee.Slug), nil
}

func toolWaitSubtasks(ctx context.Context, e *runEnv, _ json.RawMessage) (string, error) {
	n, err := e.c.store.OpenSubtasks(ctx, e.task.ID)
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", errors.New("there are no unfinished subtasks; continue the work or complete the task")
	}
	e.outcome = &outcome{kind: "wait", text: fmt.Sprintf("Waiting for %d subtask(s).", n)}
	return "Paused. You will be resumed with the subtask results.", nil
}

// --- planning ---

func taskLine(t *store.Task) string {
	who := "unassigned"
	if t.Assignee != nil {
		who = "@" + t.Assignee.Slug
	}
	return fmt.Sprintf("#%d [%s] %s (%s, %s)", t.Number, t.Status, t.Title, who, t.Priority)
}

func toolListTasks(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Status, Assignee string
		Limit            int
	}
	if err := DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	f := store.TaskFilter{OrgID: &e.task.OrgID, Limit: a.Limit}
	if f.Limit <= 0 || f.Limit > 50 {
		f.Limit = 20
	}
	if st := store.TaskStatus(strings.ToUpper(strings.TrimSpace(a.Status))); st != "" {
		if !st.Valid() {
			return "", fmt.Errorf("unknown status %q", a.Status)
		}
		f.Statuses = []store.TaskStatus{st}
	}
	if a.Assignee != "" {
		ag, err := e.resolveAgent(ctx, a.Assignee)
		if err != nil {
			return "", err
		}
		f.AssigneeID = &ag.ID
	}
	ts, err := e.c.store.ListTasks(ctx, f)
	if err != nil {
		return "", err
	}
	if len(ts) == 0 {
		return "No tasks.", nil
	}
	lines := make([]string, 0, len(ts))
	for _, t := range ts {
		lines = append(lines, taskLine(t))
	}
	return strings.Join(lines, "\n"), nil
}

func toolGetTask(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ Number int64 }
	if err := DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	t, err := e.c.store.GetTaskByNumber(ctx, e.task.OrgID, a.Number)
	if err != nil {
		return "", fmt.Errorf("task #%d not found", a.Number)
	}
	var b strings.Builder
	b.WriteString(taskLine(t) + "\n")
	if t.Description != "" {
		b.WriteString("Description: " + clip(t.Description, 1500) + "\n")
	}
	if t.Result != "" {
		b.WriteString("Result: " + clip(t.Result, 2000) + "\n")
	}
	subs, _ := e.c.store.ListTasks(ctx, store.TaskFilter{ParentID: &t.ID, All: true})
	for _, s := range subs {
		b.WriteString("  subtask " + taskLine(s))
		if s.Result != "" {
			b.WriteString(" — " + clip(s.Result, 400))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func toolCreateTask(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Title, Description, Assignee, Priority string
	}
	if err := DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	if e.subtasks >= maxSubtasksPerRun {
		return "", fmt.Errorf("task creation limit reached (%d per run)", maxSubtasksPerRun)
	}
	in := store.NewTask{OrgID: e.task.OrgID, Title: a.Title, Description: a.Description, Priority: a.Priority, CreatedBy: "agent", CreatorAgentID: &e.agent.ID}
	if a.Assignee != "" {
		ag, err := e.resolveAgent(ctx, a.Assignee)
		if err != nil {
			return "", err
		}
		in.AssigneeAgentID = &ag.ID
	}
	t, err := e.c.store.CreateTask(ctx, in)
	if err != nil {
		return "", err
	}
	e.subtasks++
	e.c.system(ctx, t.ID, fmt.Sprintf("Created by @%s while working on task #%d.", e.agent.Slug, e.task.Number))
	e.c.broker.Publish(t.OrgID, "task.updated", map[string]any{"id": t.ID, "status": t.Status})
	e.c.Wake()
	return fmt.Sprintf("Task #%d created (%s).", t.Number, t.Status), nil
}

func toolScheduleTask(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Name, Cron, Timezone, Title, Description, Assignee, Priority string
	}
	if err := DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	assignee, err := e.resolveAgent(ctx, a.Assignee)
	if err != nil {
		return "", err
	}
	creator := e.agent.ID
	sc, err := e.c.store.CreateSchedule(ctx, &store.Schedule{
		OrgID: e.task.OrgID, Name: a.Name, CronExpr: a.Cron, Timezone: a.Timezone, IsActive: true, AssigneeAgentID: &assignee.ID,
		TaskTitle: a.Title, TaskDescription: a.Description, Priority: a.Priority, CreatedBy: "agent", CreatorAgentID: &creator,
	}, e.c.ValidateSchedule)
	if err != nil {
		return "", err
	}
	e.c.schedulesChanged(ctx)
	e.c.broker.Publish(sc.OrgID, "schedule.updated", map[string]any{"id": sc.ID})
	return fmt.Sprintf("Schedule %s created: %q runs at %q (%s), assigned to @%s. The human can pause it under Schedules.",
		sc.ID, sc.Name, sc.CronExpr, sc.Timezone, assignee.Slug), nil
}

func toolListSchedules(ctx context.Context, e *runEnv, _ json.RawMessage) (string, error) {
	scs, err := e.c.store.ListSchedules(ctx, &e.task.OrgID)
	if err != nil {
		return "", err
	}
	if len(scs) == 0 {
		return "No schedules.", nil
	}
	var lines []string
	for _, s := range scs {
		state := "active"
		if !s.IsActive {
			state = "paused"
		}
		lines = append(lines, fmt.Sprintf("%s %q %s (%s) %s — task: %s", s.ID, s.Name, s.CronExpr, s.Timezone, state, s.TaskTitle))
	}
	return strings.Join(lines, "\n"), nil
}

func toolPauseSchedule(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ ID string }
	if err := DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	id, err := uuid.Parse(strings.TrimSpace(a.ID))
	if err != nil {
		return "", errors.New("id must be a schedule UUID")
	}
	sc, err := e.c.store.GetSchedule(ctx, id)
	if err != nil {
		return "", err
	}
	sc.IsActive = false
	if _, err := e.c.store.UpdateSchedule(ctx, sc, nil); err != nil {
		return "", err
	}
	e.c.schedulesChanged(ctx)
	e.c.broker.Publish(sc.OrgID, "schedule.updated", map[string]any{"id": sc.ID})
	return "Schedule paused.", nil
}

// --- memory ---

func toolRemember(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ Content string }
	if err := DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	if _, err := e.c.store.AddMemory(ctx, e.agent.ID, a.Content); err != nil {
		return "", err
	}
	e.c.broker.Publish(e.agent.OrgID, "agent.updated", map[string]any{"id": e.agent.ID})
	return "Saved.", nil
}

func toolSearchMemory(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ Query string }
	if err := DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	ms, err := e.c.store.Memories(ctx, e.agent.ID, a.Query, 20)
	if err != nil {
		return "", err
	}
	if len(ms) == 0 {
		return "No matching notes.", nil
	}
	var lines []string
	for _, m := range ms {
		lines = append(lines, fmt.Sprintf("- (%s) %s", m.CreatedAt.Format("2006-01-02"), m.Content))
	}
	return strings.Join(lines, "\n"), nil
}
