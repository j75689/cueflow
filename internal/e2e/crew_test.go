package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/guard"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

// scriptLLM routes each Chat call to the test's script, which plays every agent on the team.
type scriptLLM struct{}

var (
	scriptMu sync.Mutex
	script   func(t *testing.T, req llm.Request) llm.Message
	scriptT  *testing.T
)

func (scriptLLM) Model() string { return "script" }
func (scriptLLM) Test(context.Context) (*connector.TestResult, error) {
	return &connector.TestResult{OK: true}, nil
}
func (scriptLLM) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	if !guard.InAgent(ctx) {
		panic("agent LLM calls must carry the agent marker")
	}
	scriptMu.Lock()
	f, t := script, scriptT
	scriptMu.Unlock()
	msg := f(t, req)
	msg.Role = llm.RoleAssistant
	stop := "end"
	if len(msg.ToolCalls) > 0 {
		stop = "tool_use"
	}
	return &llm.Response{Message: msg, StopReason: stop, Usage: llm.Usage{InputTokens: 100, OutputTokens: 10}}, nil
}

func init() {
	connector.Register(connector.Spec{Type: "script_llm", Kind: connector.KindLLM, Name: "Script LLM"},
		func(connector.Config, connector.Env) (connector.Instance, error) { return scriptLLM{}, nil })
}

func setScript(t *testing.T, f func(t *testing.T, req llm.Request) llm.Message) {
	scriptMu.Lock()
	script, scriptT = f, t
	scriptMu.Unlock()
}

var callSeq int

func calls(pairs ...any) llm.Message {
	var m llm.Message
	for i := 0; i+1 < len(pairs); i += 2 {
		raw, _ := json.Marshal(pairs[i+1])
		callSeq++
		m.ToolCalls = append(m.ToolCalls, llm.ToolCall{ID: fmt.Sprintf("call_%d", callSeq), Name: pairs[i].(string), Arguments: raw})
	}
	return m
}

// whoAmI extracts the agent slug from the system prompt ("You are 🧭 Chief, an AI agent ...") via the default names.
func whoAmI(system string) string {
	for name, slug := range map[string]string{"Chief": "chief", "Researcher": "researcher", "Writer": "writer", "Editor": "editor"} {
		if strings.Contains(system, " "+name+", an AI agent") {
			return slug
		}
	}
	return "?"
}

func brief(req llm.Request) string { return req.Messages[0].Content }

func toolResults(req llm.Request) []llm.Message {
	var out []llm.Message
	for _, m := range req.Messages {
		if m.Role == llm.RoleTool {
			out = append(out, m)
		}
	}
	return out
}

func crewSetup(t *testing.T) (*env, map[string]*store.Agent) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.store.CreateConnector(ctx, "LLM", "script_llm", true, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	// Seed Chief the same way the server does on first start, then add the specialists.
	org, err := e.store.DefaultOrg(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := crew.SeedDefaults(ctx, e.store, org.ID); err != nil || n != 1 {
		t.Fatalf("seed: %d %v", n, err)
	}
	for _, a := range testSpecialists() {
		a.OrgID = org.ID
		if _, err := e.store.CreateAgent(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	agents, _ := e.store.ListAgents(ctx, org.ID)
	bySlug := map[string]*store.Agent{}
	for _, a := range agents {
		bySlug[a.Slug] = a
	}
	if len(bySlug) != 4 {
		t.Fatalf("default team: %v", bySlug)
	}
	return e, bySlug
}

func TestChiefDelegatesResearchThenWriting(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	// Some collected material for the researcher.
	var rss store.Connector
	e.do("POST", "/connectors", map[string]any{"name": "Feed", "type": "rss", "config": map[string]any{"url": "http://example.invalid/feed"}}, &rss)
	if _, err := e.cs.InsertItems(ctx, rss.ID, []connector.Item{{ExternalID: "x1", Title: "MCP 1.0 released", URL: "https://example.com/mcp", Content: "The MCP spec reached 1.0."}}); err != nil {
		t.Fatal(err)
	}

	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		b := brief(req)
		switch whoAmI(req.System) {
		case "chief":
			if !strings.Contains(req.System, "@writer") || !strings.Contains(req.System, "@researcher") {
				t.Errorf("chief should see its team in the system prompt")
			}
			switch {
			case !strings.Contains(b, "## Subtasks"):
				return calls("create_subtask", map[string]any{"title": "Research the latest MCP news", "description": "Find the key points, with sources", "assignee": "researcher"},
					"wait_for_subtasks", map[string]any{})
			case !strings.Contains(b, "@writer"):
				if !strings.Contains(b, "MCP 1.0 is out") {
					t.Errorf("chief should be resumed with the research result:\n%s", b)
				}
				return calls("create_subtask", map[string]any{"title": "Write a Threads draft", "description": "Based on the research: MCP 1.0 is out", "assignee": "writer"},
					"wait_for_subtasks", map[string]any{})
			default:
				return calls("complete_task", map[string]any{"summary": "Research and draft are done; the draft is waiting for review."})
			}
		case "researcher":
			if len(toolResults(req)) == 0 {
				// Tries to publish (must be refused) and searches collected material.
				return calls("integrationSchedulePostTool", map[string]any{"socialPost": []any{}},
					"search_materials", map[string]any{"query": "MCP"})
			}
			for _, m := range toolResults(req) {
				if strings.Contains(m.Content, "not available to you") && !m.IsError {
					t.Errorf("blocked tool must be an error result")
				}
			}
			if !strings.Contains(toolResults(req)[1].Content, "MCP 1.0 released") {
				t.Errorf("search_materials did not return the item: %s", toolResults(req)[1].Content)
			}
			return calls("complete_task", map[string]any{"summary": "MCP 1.0 is out (https://example.com/mcp)"})
		case "writer":
			if len(toolResults(req)) == 0 {
				return calls("create_post_draft", map[string]any{"platform": "threads", "title": "MCP 1.0",
					"content": "MCP 1.0 is finally out. Is your team ready?", "hashtags": []string{"MCP"}, "source_urls": []string{"https://example.com/mcp"}})
			}
			return calls("complete_task", map[string]any{"summary": "Created a Threads draft: " + toolResults(req)[0].Content})
		}
		t.Errorf("unexpected agent: %s", req.System[:80])
		return llm.Message{Content: "?"}
	})

	var task store.Task
	if code := e.do("POST", "/tasks", map[string]any{"title": "Write a Threads post about MCP", "assignee_agent_id": team["chief"].ID}, &task); code != 201 {
		t.Fatalf("create task: %d", code)
	}
	if task.Status != store.TaskTodo {
		t.Fatalf("assigned task should start in TODO, got %s", task.Status)
	}
	e.crew.RunPending(ctx, 10)

	var got struct {
		store.Task
		Subtasks     []store.Task      `json:"subtasks"`
		Deliverables []deliverable.Ref `json:"deliverables"`
	}
	e.do("GET", "/tasks/"+task.ID.String(), nil, &got)
	if got.Status != store.TaskReview || !strings.Contains(got.Result, "waiting for review") {
		t.Fatalf("chief task should be in REVIEW with a result: %s %q", got.Status, got.Result)
	}
	if len(got.Subtasks) != 2 {
		t.Fatalf("subtasks: %+v", got.Subtasks)
	}
	for _, s := range got.Subtasks {
		if s.Status != store.TaskDone || s.CreatedBy != "agent" {
			t.Errorf("agent-created subtask should end DONE: #%d %s %s", s.Number, s.Status, s.CreatedBy)
		}
	}

	// The writer's draft is in the review queue, linked to its subtask, and was not published.
	var posts struct {
		Items []deliverable.Deliverable `json:"items"`
	}
	e.do("GET", "/deliverables?status=PENDING_REVIEW&kind=post", nil, &posts)
	if len(posts.Items) != 1 || posts.Items[0].TaskID == nil {
		t.Fatalf("draft not queued for review: %+v", posts.Items)
	}
	if len(e.mock.Posts()) != 0 {
		t.Fatal("nothing may be published without human approval")
	}

	// The researcher's attempt to call a publishing tool is visible in its thread.
	var research store.Task
	for _, s := range got.Subtasks {
		if strings.Contains(s.Title, "Research") {
			research = s
		}
	}
	var msgs []store.TaskMessage
	e.do("GET", "/tasks/"+research.ID.String()+"/messages", nil, &msgs)
	var thread []string
	for _, m := range msgs {
		thread = append(thread, m.Kind+":"+m.Content)
	}
	joined := strings.Join(thread, "\n")
	if !strings.Contains(joined, "not available to you") || !strings.Contains(joined, "result:MCP 1.0 is out") {
		t.Fatalf("research thread:\n%s", joined)
	}

	var runs []store.AgentRun
	e.do("GET", "/tasks/"+task.ID.String()+"/runs", nil, &runs)
	if len(runs) != 3 {
		t.Fatalf("chief should have run 3 times (delegate, delegate, complete), got %d", len(runs))
	}
	if runs[0].Model != "script" || runs[0].Runtime != "builtin" || runs[0].ConnectorID == nil {
		t.Fatalf("run provenance not recorded: %+v", runs[0])
	}

	// Usage: chief 3 turns, researcher 2, writer 2 = 7 LLM calls x (100 in, 10 out).
	var usage store.Usage
	if code := e.do("GET", "/usage?days=7&tz=Asia/Taipei", nil, &usage); code != 200 {
		t.Fatalf("usage: %d", code)
	}
	if usage.Totals.Runs != 5 || usage.Totals.InputTokens != 700 || usage.Totals.OutputTokens != 70 || usage.Totals.UnreportedRuns != 0 {
		t.Fatalf("totals: %+v", usage.Totals)
	}
	if len(usage.Daily) != 7 || usage.Daily[6].InputTokens != 700 {
		t.Fatalf("daily should be 7 zero-filled days ending today: %+v", usage.Daily)
	}
	if len(usage.ByAgent) != 3 || usage.ByAgent[0].Slug != "chief" || usage.ByAgent[0].InputTokens != 300 {
		t.Fatalf("by agent: %+v", usage.ByAgent)
	}
	if len(usage.ByModel) != 1 || usage.ByModel[0].ConnectorType != "script_llm" || usage.ByModel[0].Model != "script" {
		t.Fatalf("by model: %+v", usage.ByModel)
	}
	if len(usage.TopTasks) == 0 || usage.TopTasks[0].TaskID != task.ID {
		t.Fatalf("top tasks: %+v", usage.TopTasks)
	}
	if code := e.do("GET", "/usage?days=0", nil, nil); code != 400 {
		t.Fatalf("days=0 should be rejected: %d", code)
	}
}

func TestAskHumanScheduleAndMention(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		b := brief(req)
		switch whoAmI(req.System) {
		case "chief":
			if !strings.Contains(b, "8 in the morning") {
				return calls("ask_human", map[string]any{"question": "What time should I prepare it?"})
			}
			res := toolResults(req)
			switch len(res) {
			case 0:
				return calls("schedule_task", map[string]any{"cron": "* * * * *", "title": "AI news", "description": "x", "assignee": "writer"})
			case 1:
				if !res[0].IsError || !strings.Contains(res[0].Content, "5 minutes") {
					t.Errorf("every-minute schedule should be rejected: %+v", res[0])
				}
				return calls("schedule_task", map[string]any{"name": "Daily AI news", "cron": "0 8 * * *", "timezone": "Asia/Taipei",
					"title": "Summarize AI news as a Threads draft", "description": "Collect yesterday's AI news and write a draft", "assignee": "writer"},
					"remember", map[string]any{"content": "The user wants AI news drafts every day at 08:00"})
			default:
				return calls("complete_task", map[string]any{"summary": "Scheduled @writer to summarize AI news every day at 08:00 (Asia/Taipei)."})
			}
		case "editor":
			return llm.Message{Content: "I reviewed it; nothing needs to change."}
		}
		return calls("complete_task", map[string]any{"summary": "ok"})
	})

	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Summarize AI news for me every day", "assignee_agent_id": team["chief"].ID}, &task)
	e.crew.RunPending(ctx, 5)
	got, _ := e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskWaitingHuman {
		t.Fatalf("agent asked a question; want WAITING_HUMAN, got %s", got.Status)
	}

	// Human answers in the thread -> back to TODO -> the chief continues.
	var msg store.TaskMessage
	if code := e.do("POST", "/tasks/"+task.ID.String()+"/messages", map[string]any{"content": "8 in the morning"}, &msg); code != 201 {
		t.Fatalf("post message: %d", code)
	}
	got, _ = e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskTodo {
		t.Fatalf("a reply should requeue the task, got %s", got.Status)
	}
	e.crew.RunPending(ctx, 5)
	got, _ = e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskReview {
		t.Fatalf("want REVIEW, got %s", got.Status)
	}

	var schedules []store.Schedule
	e.do("GET", "/schedules", nil, &schedules)
	if len(schedules) != 1 || schedules[0].CreatedBy != "agent" || schedules[0].Timezone != "Asia/Taipei" ||
		schedules[0].AssigneeAgentID == nil || *schedules[0].AssigneeAgentID != team["writer"].ID {
		t.Fatalf("schedule: %+v", schedules)
	}
	mems, _ := e.store.Memories(ctx, team["chief"].ID, "08:00", 10)
	if len(mems) != 1 {
		t.Fatalf("chief should have remembered the preference: %+v", mems)
	}

	// "Run now" on the schedule spawns a task for the writer.
	var spawned map[string]uuid.UUID
	if code := e.do("POST", "/schedules/"+schedules[0].ID.String()+"/run", nil, &spawned); code != 202 {
		t.Fatalf("run schedule: %d", code)
	}
	st, _ := e.store.GetTask(ctx, spawned["task_id"])
	if st.Status != store.TaskTodo || st.AssigneeAgentID == nil || *st.AssigneeAgentID != team["writer"].ID || st.ScheduleID == nil {
		t.Fatalf("spawned task: %+v", st)
	}

	// "@editor ..." hands the finished task to the editor, who replies with plain text.
	e.do("POST", "/tasks/"+task.ID.String()+"/messages", map[string]any{"content": "@editor please check the schedule"}, nil)
	got, _ = e.store.GetTask(ctx, task.ID)
	if got.AssigneeAgentID == nil || *got.AssigneeAgentID != team["editor"].ID || got.Status != store.TaskTodo {
		t.Fatalf("mention should reassign to editor and requeue: %+v", got)
	}
	e.crew.RunPending(ctx, 3)
	got, _ = e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskReview || !strings.Contains(got.Result, "nothing needs to change") {
		t.Fatalf("editor's plain reply should complete the task: %s %q", got.Status, got.Result)
	}
}

func TestBoardRules(t *testing.T) {
	e, team := crewSetup(t)
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "idea"}, &task)
	if task.Status != store.TaskBacklog {
		t.Fatalf("unassigned task should start in BACKLOG, got %s", task.Status)
	}
	if code := e.do("PATCH", "/tasks/"+task.ID.String(), map[string]any{"status": "IN_PROGRESS"}, nil); code != 400 {
		t.Fatalf("humans cannot set IN_PROGRESS: %d", code)
	}
	if code := e.do("POST", "/tasks/"+task.ID.String()+"/run", nil, nil); code != 400 {
		t.Fatalf("run without assignee: %d", code)
	}
	var moved store.Task
	e.do("PATCH", "/tasks/"+task.ID.String(), map[string]any{"assignee_agent_id": team["writer"].ID, "status": "TODO", "position": 5.5}, &moved)
	if moved.Status != store.TaskTodo || moved.Position != 5.5 || moved.Assignee == nil || moved.Assignee.Slug != "writer" {
		t.Fatalf("patch: %+v", moved)
	}
	var list struct {
		Items []store.Task `json:"items"`
	}
	e.do("GET", "/tasks?assignee="+team["writer"].ID.String(), nil, &list)
	if len(list.Items) != 1 {
		t.Fatalf("filter by assignee: %d", len(list.Items))
	}
	var bad struct {
		Error struct{ Code string } `json:"error"`
	}
	if code := e.do("POST", "/agents", map[string]any{"name": "X", "slug": "writer"}, &bad); code != 400 {
		t.Fatalf("duplicate slug: %d %+v", code, bad)
	}
	if code := e.do("POST", "/agents", map[string]any{"name": "X", "capabilities": []string{"publish"}}, nil); code != 400 {
		t.Fatalf("unknown capability accepted: %d", code)
	}
	var meta struct {
		AgentCapabilities []struct {
			ID    string
			Tools []string
		} `json:"agent_capabilities"`
	}
	e.do("GET", "/meta", nil, &meta)
	for _, c := range meta.AgentCapabilities {
		for _, tl := range c.Tools {
			if strings.Contains(tl, "publish") || strings.Contains(tl, "Schedule") {
				t.Errorf("capability %s exposes a publishing tool: %s", c.ID, tl)
			}
		}
	}
}
