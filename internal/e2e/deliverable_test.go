package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/guard"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

// actionServer is a fake action MCP server: its "deliver" tool records what it was handed and
// answers with the contract's shape. fail makes it refuse every call.
type actionServer struct {
	mu       sync.Mutex
	received []map[string]any
	fail     atomic.Bool
	counter  atomic.Int32
}

func (a *actionServer) start(t *testing.T) *httptest.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "filer", Version: "test"}, nil)
	srv.AddTool(&mcp.Tool{Name: "deliver", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if a.fail.Load() {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "simulated outage"}}}, nil
			}
			var in map[string]any
			_ = json.Unmarshal(req.Params.Arguments, &in)
			a.mu.Lock()
			a.received = append(a.received, in)
			a.mu.Unlock()
			n := a.counter.Add(1)
			b, _ := json.Marshal(map[string]any{"external_ids": map[string]string{"page": "wiki-" + string(rune('0'+n))},
				"details": map[string]any{"url": "https://wiki.example/p/" + string(rune('0'+n))}})
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func (a *actionServer) calls() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]map[string]any(nil), a.received...)
}

func TestDeliverablesReviewAndAct(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	fake := &actionServer{}
	ts := fake.start(t)

	// --- the module is mounted and the connector kind exists ---
	var meta struct {
		Modules []string            `json:"modules"`
		Kinds   []deliverable.Kind  `json:"deliverable_kinds"`
		Types   []connector.Spec    `json:"connector_types"`
		Trans   map[string][]string `json:"deliverable_transitions"`
	}
	e.do("GET", "/meta", nil, &meta)
	if !containsString(meta.Modules, "deliverables") || len(meta.Kinds) == 0 || meta.Kinds[0].ID != "document" || len(meta.Trans["QUEUED"]) != 2 {
		t.Fatalf("meta: %+v", meta)
	}
	hasAction := false
	for _, s := range meta.Types {
		hasAction = hasAction || (s.Type == "mcp_action" && s.Kind == connector.KindAction)
	}
	if !hasAction {
		t.Fatal("mcp_action connector type not registered")
	}

	var action store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "Wiki", "type": "mcp_action",
		"config": map[string]any{"endpoint_url": ts.URL, "auth_token": "t"}}, &action); code != 201 {
		t.Fatalf("create action connector: %d", code)
	}
	var test connector.TestResult
	e.do("POST", "/connectors/"+action.ID.String()+"/test", nil, &test)
	if !test.OK {
		t.Fatalf("action test: %+v", test)
	}

	// --- validation ---
	var apiErr struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if code := e.do("POST", "/deliverables", map[string]any{"kind": "document", "title": "x", "payload": map[string]any{}}, &apiErr); code != 400 ||
		!strings.Contains(apiErr.Error.Message, "content is required") {
		t.Fatalf("empty document accepted: %d %+v", code, apiErr)
	}
	if code := e.do("POST", "/deliverables", map[string]any{"kind": "spreadsheet", "title": "x", "payload": map[string]any{"a": 1}}, &apiErr); code != 400 ||
		!strings.Contains(apiErr.Error.Message, "unknown deliverable kind") {
		t.Fatalf("unknown kind accepted: %d %+v", code, apiErr)
	}

	// --- create, edit, approve, act ---
	var d deliverable.Deliverable
	if code := e.do("POST", "/deliverables", map[string]any{"kind": "document", "title": "Launch plan",
		"payload": map[string]any{"content": "# Plan\n\nShip it."}, "submit": true}, &d); code != 201 || d.Status != deliverable.StatusPendingReview {
		t.Fatalf("create: %d %+v", code, d)
	}
	if code := e.do("PATCH", "/deliverables/"+d.ID.String(), map[string]any{"version": d.Version - 1, "title": "x"}, nil); code != 409 {
		t.Fatalf("stale version: %d", code)
	}
	var edited deliverable.Deliverable
	if code := e.do("PATCH", "/deliverables/"+d.ID.String(), map[string]any{"version": d.Version,
		"payload": map[string]any{"content": "# Plan\n\nShip it on Friday."}}, &edited); code != 200 || edited.Version != d.Version+1 {
		t.Fatalf("edit: %d %+v", code, edited)
	}
	if code := e.do("POST", "/deliverables/"+d.ID.String()+"/retry", nil, &apiErr); code != 409 || apiErr.Error.Code != "invalid_transition" {
		t.Fatalf("retry from PENDING_REVIEW: %d %+v", code, apiErr)
	}
	if code := e.do("POST", "/deliverables/"+d.ID.String()+"/approve", map[string]any{"note": "lgtm"}, &d); code != 200 || d.Status != deliverable.StatusApproved {
		t.Fatalf("approve: %d %+v", code, d)
	}
	e.deliverer.Tick(ctx)
	e.do("GET", "/deliverables/"+d.ID.String(), nil, &d)
	if d.Status != deliverable.StatusDone || d.ExternalIDs["page"] != "wiki-1" || d.ActedAt == nil || !strings.Contains(string(d.Result), "wiki.example") {
		t.Fatalf("not done: %+v", d)
	}
	got := fake.calls()
	if len(got) != 1 || got[0]["kind"] != "document" || got[0]["title"] != "Launch plan" ||
		!strings.Contains(got[0]["payload"].(map[string]any)["content"].(string), "Friday") {
		t.Fatalf("action server received %+v", got)
	}
	var audit []deliverable.AuditLog
	e.do("GET", "/deliverables/"+d.ID.String()+"/audit", nil, &audit)
	var actions []string
	for _, a := range audit {
		actions = append(actions, string(a.Action))
	}
	if want := "CREATED,SUBMITTED,EDITED,APPROVED,QUEUED,STARTED,DONE"; strings.Join(actions, ",") != want {
		t.Fatalf("audit = %s, want %s", strings.Join(actions, ","), want)
	}
	if code := e.do("PATCH", "/deliverables/"+d.ID.String(), map[string]any{"version": d.Version, "title": "x"}, nil); code != 400 {
		t.Fatalf("editing a done item: %d", code)
	}

	// --- failure, then a manual retry ---
	var f deliverable.Deliverable
	e.do("POST", "/deliverables", map[string]any{"kind": "document", "title": "Retro", "payload": map[string]any{"content": "notes"}, "submit": true}, &f)
	fake.fail.Store(true)
	e.do("POST", "/deliverables/"+f.ID.String()+"/approve", nil, nil)
	e.deliverer.Tick(ctx)
	e.do("GET", "/deliverables/"+f.ID.String(), nil, &f)
	if f.Status != deliverable.StatusFailed || !strings.Contains(f.ErrorMessage, "simulated outage") {
		t.Fatalf("expected FAILED with the reason: %+v", f)
	}
	fake.fail.Store(false)
	if code := e.do("POST", "/deliverables/"+f.ID.String()+"/retry", nil, nil); code != 200 {
		t.Fatalf("retry: %d", code)
	}
	e.deliverer.Tick(ctx)
	e.do("GET", "/deliverables/"+f.ID.String(), nil, &f)
	if f.Status != deliverable.StatusDone || f.Attempts != 2 || f.ErrorMessage != "" {
		t.Fatalf("retry did not finish: %+v", f)
	}

	// --- without an action connector, approval is the end: DONE with nothing run ---
	e.do("PATCH", "/connectors/"+action.ID.String(), map[string]any{"is_active": false}, nil)
	var n deliverable.Deliverable
	e.do("POST", "/deliverables", map[string]any{"kind": "document", "title": "Memo", "payload": map[string]any{"content": "fyi"}, "submit": true}, &n)
	e.do("POST", "/deliverables/"+n.ID.String()+"/approve", nil, nil)
	before := len(fake.calls())
	e.deliverer.Tick(ctx)
	e.do("GET", "/deliverables/"+n.ID.String(), nil, &n)
	if n.Status != deliverable.StatusDone || len(n.ExternalIDs) != 0 || len(fake.calls()) != before {
		t.Fatalf("approval without an action connector: %+v (calls %d -> %d)", n, before, len(fake.calls()))
	}

	// --- the guard: an action connector refuses a call that carries no grant ---
	inst, err := connector.Build("mcp_action", connector.Config{"endpoint_url": ts.URL}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst.(connector.Actor).Act(ctx, connector.ActRequest{DeliverableID: uuid.New(), Kind: "document", Payload: json.RawMessage(`{}`)})
	if !errors.Is(err, guard.ErrPublishForbidden) {
		t.Fatalf("unguarded act: %v", err)
	}
	_, err = inst.(connector.Actor).Act(guard.WithAgent(guard.WithPublishGrant(ctx, d.ID)), connector.ActRequest{DeliverableID: d.ID})
	if !errors.Is(err, guard.ErrPublishForbidden) {
		t.Fatalf("act from an agent context: %v", err)
	}

	// --- stats, listing and the MCP tool ---
	var stats struct {
		ByStatus map[string]int `json:"deliverables_by_status"`
	}
	e.do("GET", "/stats", nil, &stats)
	if stats.ByStatus["DONE"] != 3 {
		t.Fatalf("stats: %+v", stats)
	}
	var list struct {
		Items []deliverable.Deliverable `json:"items"`
		Total int                       `json:"total"`
	}
	e.do("GET", "/deliverables?status=DONE&kind=document", nil, &list)
	if list.Total != 3 {
		t.Fatalf("list: %+v", list)
	}
	cs := mcpSession(t, e)
	if out, isErr := callTool(t, cs, "list_deliverables", map[string]any{"status": "DONE"}); isErr || !strings.Contains(out, `"total": 3`) {
		t.Fatalf("list_deliverables: %s", out)
	}
}

// An agent with the deliverables capability writes a document for its task; it lands in review,
// the task shows it, and the next run's brief lists it.
func TestAgentCreatesDeliverable(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	chief := team["chief"]
	if !chief.Has("deliverables") {
		t.Fatalf("the starter Chief should have the deliverables capability: %v", chief.Capabilities)
	}
	var briefs []string
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		briefs = append(briefs, brief(req))
		if len(toolResults(req)) == 0 {
			return calls("create_deliverable", map[string]any{"title": "Weekly report",
				"payload": map[string]any{"content": "# Week 42\n\nAll green.", "summary": "nothing on fire"}})
		}
		return calls("complete_task", map[string]any{"summary": "report drafted"})
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Write the weekly report", "assignee_agent_id": chief.ID}, &task)
	e.crew.RunPending(ctx, 2)

	refs, err := e.ds.ForTask(ctx, task.ID)
	if err != nil || len(refs) != 1 || refs[0].Status != deliverable.StatusPendingReview || refs[0].Kind != "document" {
		t.Fatalf("deliverables for task: %+v %v", refs, err)
	}
	var detail struct {
		Deliverables []deliverable.Ref `json:"deliverables"`
	}
	e.do("GET", "/tasks/"+task.ID.String(), nil, &detail)
	if len(detail.Deliverables) != 1 || detail.Deliverables[0].Title != "Weekly report" {
		t.Fatalf("task detail: %+v", detail)
	}
	d, _ := e.ds.Get(ctx, refs[0].ID)
	if d.CreatedBy != deliverable.ActorAgent || d.TaskID == nil || *d.TaskID != task.ID {
		t.Fatalf("deliverable: %+v", d)
	}
	// A later run of the same task sees what was already produced.
	e.do("POST", "/tasks/"+task.ID.String()+"/messages", map[string]any{"content": "add the risks section"}, nil)
	e.crew.RunPending(ctx, 2)
	last := briefs[len(briefs)-1]
	if !strings.Contains(last, "Deliverables created for this task") || !strings.Contains(last, "Weekly report") {
		t.Fatalf("brief does not list the deliverable:\n%s", last)
	}
}

// An agent reads a deliverable in full with get_deliverable: a short one in one call, a long one in
// pages by offset (sized to fit with a long header), a failed one with its error, and none of another
// org's.
func TestAgentReadsDeliverable(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "File the issue", "assignee_agent_id": team["chief"].ID}, &task)
	doc := func(org uuid.UUID, title, content, summary string) uuid.UUID {
		raw, _ := json.Marshal(map[string]any{"content": content, "summary": summary})
		d, err := e.ds.Create(ctx, deliverable.New{OrgID: org, Kind: deliverable.DocumentKind, Title: title, Payload: raw}, deliverable.ActorAgent, true)
		if err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	long := "# 設計\n\n" + strings.Repeat("Disney+ 字幕。", 3000) + "\n## Last section\nthe end"
	short := doc(task.OrgID, "Short", "# Short\n\nAll of it.", "the plan")
	big := doc(task.OrgID, "Long", long, strings.Repeat("摘要", 1050)) // a header past the old 2,000 characters of slack
	failed := doc(task.OrgID, "Failed", "# Failed", "")
	if _, err := e.pool.Exec(ctx, `UPDATE deliverables SET status='FAILED', error_message='github: 422 Validation Failed' WHERE id=$1`, failed); err != nil {
		t.Fatal(err)
	}
	other, err := e.store.CreateOrg(ctx, &store.Org{Name: "Other", Slug: "other"})
	if err != nil {
		t.Fatal(err)
	}
	foreign := doc(other.ID, "Theirs", "not yours", "")

	var results []llm.Message
	next := regexp.MustCompile(`Call get_deliverable with offset (\d+) for the rest`)
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		results = toolResults(req)
		if len(results) == 0 {
			return calls("get_deliverable", map[string]any{"deliverable_id": short.String()},
				"get_deliverable", map[string]any{"deliverable_id": failed.String()},
				"get_deliverable", map[string]any{"deliverable_id": foreign.String()},
				"get_deliverable", map[string]any{"deliverable_id": big.String()})
		}
		if m := next.FindStringSubmatch(results[len(results)-1].Content); m != nil && len(results) < 10 {
			off, _ := strconv.Atoi(m[1])
			return calls("get_deliverable", map[string]any{"deliverable_id": big.String(), "offset": off})
		}
		return calls("complete_task", map[string]any{"summary": "read"})
	})
	e.crew.RunPending(ctx, 2)
	if len(results) < 5 {
		t.Fatalf("tool results: %+v", results)
	}
	if r := results[0].Content; !strings.Contains(r, "[PENDING_REVIEW] document, version 2: Short") ||
		!strings.Contains(r, "Summary: the plan") || !strings.Contains(r, "characters 0–19 of 19.\n") ||
		strings.Contains(r, "offset") || !strings.HasSuffix(r, "# Short\n\nAll of it.") {
		t.Fatalf("short deliverable: %q", r)
	}
	if r := results[1].Content; !strings.Contains(r, "[FAILED]") || !strings.Contains(r, "Error: github: 422 Validation Failed") {
		t.Fatalf("failed deliverable: %q", r)
	}
	if r := results[2].Content; !strings.HasPrefix(r, "error:") || strings.Contains(r, "not yours") {
		t.Fatalf("another org's deliverable: %q", r)
	}
	// The pages of the long one each fit what the model sees, and add up to the content.
	var got strings.Builder
	for i, p := range results[3:] {
		if n := utf8.RuneCountInString(p.Content); n > 32000 || strings.Contains(p.Content, "(truncated") {
			t.Fatalf("page %d is %d characters, or was clipped: %.300q", i, n, p.Content)
		}
		if i == 0 && !strings.Contains(p.Content, "Summary: "+strings.Repeat("摘要", 1050)+"\n") {
			t.Fatalf("the first page lost the summary: %.300q", p.Content)
		}
		_, body, _ := strings.Cut(p.Content[strings.Index(p.Content, "Content: characters"):], "\n\n")
		got.WriteString(body)
	}
	if len(results) != 5 || got.String() != long {
		t.Fatalf("%d pages add up to %d of %d characters", len(results)-3, utf8.RuneCountInString(got.String()), utf8.RuneCountInString(long))
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
