package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/jobs"
	"github.com/j75689/cueflow/internal/store"
)

// POST /tasks/bulk moves or deletes many tasks in one request, each through the single endpoint's
// op: one that fails (missing, in another org, a move PATCH refuses) fails alone with PATCH's or
// DELETE's code, the rest go through, and the response says which was which.
func TestTaskBulk(t *testing.T) {
	e := setup(t)
	newTask := func(body map[string]any) store.Task {
		t.Helper()
		var task store.Task
		if code := e.do("POST", "/tasks", body, &task); code != 201 {
			t.Fatalf("create %v: %d", body["title"], code)
		}
		return task
	}
	type result struct {
		OK     []uuid.UUID `json:"ok"`
		Failed []struct {
			ID, Code, Message string
		} `json:"failed"`
	}

	a := newTask(map[string]any{"title": "a", "status": "BACKLOG"})
	b := newTask(map[string]any{"title": "b", "status": "BACKLOG"})
	var dev store.Org
	if code := e.do("POST", "/orgs", map[string]any{"name": "Other"}, &dev); code != 201 {
		t.Fatalf("create org: %d", code)
	}
	var other store.Task
	if code := e.do("POST", "/tasks?org="+dev.Slug, map[string]any{"title": "other", "status": "BACKLOG"}, &other); code != 201 {
		t.Fatalf("create other-org task: %d", code)
	}

	// b appears twice, the second time upper-cased: one move, no failure for the duplicate. The
	// other org's task is not found from the default org, and stays where it was.
	var res result
	ids := []string{a.ID.String(), b.ID.String(), strings.ToUpper(b.ID.String()), other.ID.String(), uuid.NewString(), "not-a-uuid"}
	if code := e.do("POST", "/tasks/bulk", map[string]any{"action": "move", "ids": ids, "status": "REVIEW"}, &res); code != 200 {
		t.Fatalf("bulk move: %d", code)
	}
	if len(res.OK) != 2 || res.OK[0] != a.ID || res.OK[1] != b.ID {
		t.Fatalf("ok: %v", res.OK)
	}
	if len(res.Failed) != 3 || res.Failed[0].ID != other.ID.String() || res.Failed[0].Code != "not_found" ||
		res.Failed[1].Code != "not_found" || res.Failed[2].ID != "not-a-uuid" || res.Failed[2].Code != "not_found" {
		t.Fatalf("failed: %+v", res.Failed)
	}
	var got store.Task
	for _, task := range []store.Task{a, b} {
		if e.do("GET", "/tasks/"+task.ID.String(), nil, &got); got.Status != store.TaskReview {
			t.Fatalf("%s after bulk move: %s", task.Title, got.Status)
		}
	}
	var msgs []store.TaskMessage
	if e.do("GET", "/tasks/"+a.ID.String()+"/messages", nil, &msgs); len(msgs) == 0 || msgs[len(msgs)-1].Content != "Moved to REVIEW by a human." {
		t.Fatalf("messages: %+v", msgs)
	}
	if e.do("GET", "/tasks/"+other.ID.String(), nil, &got); got.Status != store.TaskBacklog {
		t.Fatalf("other-org task after bulk move: %s", got.Status)
	}

	// A move PATCH refuses fails per id, still with 200: the caller reads the per-id results.
	if code := e.do("POST", "/tasks/bulk", map[string]any{"action": "move", "ids": []string{a.ID.String(), b.ID.String()}, "status": "IN_PROGRESS"}, &res); code != 200 ||
		len(res.OK) != 0 || len(res.Failed) != 2 || res.Failed[0].Code != "invalid_argument" {
		t.Fatalf("bulk move to IN_PROGRESS: %d %+v", code, res)
	}

	// Deleting a parent deletes its subtask too, so the subtask, named after it, is not found.
	parent := newTask(map[string]any{"title": "parent", "status": "BACKLOG"})
	child := newTask(map[string]any{"title": "child", "status": "BACKLOG", "parent_id": parent.ID})
	ids = []string{parent.ID.String(), child.ID.String(), a.ID.String(), other.ID.String()}
	if code := e.do("POST", "/tasks/bulk", map[string]any{"action": "delete", "ids": ids}, &res); code != 200 {
		t.Fatalf("bulk delete: %d", code)
	}
	if len(res.OK) != 2 || res.OK[0] != parent.ID || res.OK[1] != a.ID ||
		len(res.Failed) != 2 || res.Failed[0].ID != child.ID.String() || res.Failed[0].Code != "not_found" ||
		res.Failed[1].ID != other.ID.String() || res.Failed[1].Code != "not_found" {
		t.Fatalf("bulk delete: %+v", res)
	}
	for _, task := range []store.Task{parent, child, a} {
		if code := e.do("GET", "/tasks/"+task.ID.String(), nil, nil); code != 404 {
			t.Fatalf("%s after bulk delete: %d", task.Title, code)
		}
	}
	for _, task := range []store.Task{b, other} {
		if code := e.do("GET", "/tasks/"+task.ID.String(), nil, nil); code != 200 {
			t.Fatalf("%s after bulk delete: %d", task.Title, code)
		}
	}

	for name, body := range map[string]map[string]any{
		"no action":   {"ids": []string{b.ID.String()}},
		"bad action":  {"action": "archive", "ids": []string{b.ID.String()}},
		"no status":   {"action": "move", "ids": []string{b.ID.String()}},
		"bad status":  {"action": "move", "ids": []string{b.ID.String()}, "status": "LATER"},
		"no ids":      {"action": "delete", "ids": []string{}},
		"too many":    {"action": "delete", "ids": make([]string, 501)},
		"ids missing": {"action": "delete"},
	} {
		if code := e.do("POST", "/tasks/bulk", body, nil); code != 400 {
			t.Fatalf("%s: %d", name, code)
		}
	}
}

// A database error while looking a task up stops the batch as internal, not as not_found: the board
// counts a delete's not_found as done, so a lookup that failed must not read as a task already gone.
func TestTaskBulkStopsOnDatabaseError(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var a, b store.Task
	e.do("POST", "/tasks", map[string]any{"title": "a"}, &a)
	e.do("POST", "/tasks", map[string]any{"title": "b"}, &b)

	// Another transaction holds the tasks table, so the lookup outlives the request's deadline.
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "LOCK TABLE tasks IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"action": "delete", "ids": []string{a.ID.String(), b.ID.String()}})
	req := httptest.NewRequestWithContext(rctx, "POST", "/api/v1/tasks/bulk", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.server.Handler().ServeHTTP(rec, req)
	_ = tx.Rollback(ctx)

	var res struct {
		OK     []uuid.UUID `json:"ok"`
		Failed []struct{ ID, Code string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); rec.Code != 200 || err != nil {
		t.Fatalf("bulk delete: %d %s", rec.Code, rec.Body)
	}
	if len(res.OK) != 0 || len(res.Failed) != 2 || res.Failed[0].ID != a.ID.String() || res.Failed[0].Code != "internal" ||
		res.Failed[1].ID != b.ID.String() || res.Failed[1].Code != "not_attempted" {
		t.Fatalf("bulk delete under a lock: %+v", res)
	}
	for _, task := range []store.Task{a, b} {
		if code := e.do("GET", "/tasks/"+task.ID.String(), nil, nil); code != 200 {
			t.Fatalf("%s after the failed bulk delete: %d", task.Title, code)
		}
	}
}

// Bulk-moving assigned cards to To do queues their runs like PATCH does, with the system message,
// but wakes the crew once for the batch: with a queue, a wake-up is a Cloud Tasks call.
func TestTaskBulkMoveAssignedToTodo(t *testing.T) {
	e, team := crewSetup(t)
	q := &fakeQueue{}
	e.crew.Queue = q
	var a, b store.Task
	for _, task := range []*store.Task{&a, &b} {
		if code := e.do("POST", "/tasks", map[string]any{"title": "assigned", "status": "BACKLOG", "assignee_agent_id": team["chief"].ID}, task); code != 201 {
			t.Fatalf("create: %d", code)
		}
	}
	before := q.count(jobs.KindCrew)

	var res struct {
		OK []uuid.UUID `json:"ok"`
	}
	if code := e.do("POST", "/tasks/bulk", map[string]any{"action": "move", "ids": []string{a.ID.String(), b.ID.String()}, "status": "TODO"}, &res); code != 200 || len(res.OK) != 2 {
		t.Fatalf("bulk move to TODO: %d %+v", code, res)
	}
	if n := q.count(jobs.KindCrew) - before; n != 1 {
		t.Fatalf("crew wake-ups for the batch: %d", n)
	}
	for _, task := range []store.Task{a, b} {
		var got store.Task
		if e.do("GET", "/tasks/"+task.ID.String(), nil, &got); got.Status != store.TaskTodo {
			t.Fatalf("after bulk move: %s", got.Status)
		}
		var msgs []store.TaskMessage
		if e.do("GET", "/tasks/"+task.ID.String()+"/messages", nil, &msgs); len(msgs) == 0 || msgs[len(msgs)-1].Content != "Moved to TODO by a human." {
			t.Fatalf("messages: %+v", msgs)
		}
	}
}
