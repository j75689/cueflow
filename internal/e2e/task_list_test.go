package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/store"
)

// ListTasks pages with Limit/Offset in a deterministic order: Sort recent is most recently finished
// first with ties broken by id DESC, Sort position breaks equal positions by id.
func TestTaskListPaging(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var ids []uuid.UUID
	for i := range 7 {
		task, err := e.store.CreateTask(ctx, store.NewTask{Title: fmt.Sprintf("done %d", i), Status: store.TaskDone})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	// Task i finished i hours ago, except tasks 3 and 4 which share a timestamp; every task shares one
	// position and created_at.
	base := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	at := map[uuid.UUID]time.Time{}
	for i, id := range ids {
		hours := i
		if i == 4 {
			hours = 3
		}
		at[id] = base.Add(-time.Duration(hours) * time.Hour)
		if _, err := e.pool.Exec(ctx, "UPDATE tasks SET completed_at=$2, position=1, created_at=$3 WHERE id=$1", id, at[id], base); err != nil {
			t.Fatal(err)
		}
	}
	byID := func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }
	recent := slices.Clone(ids)
	slices.SortFunc(recent, func(a, b uuid.UUID) int {
		if c := at[b].Compare(at[a]); c != 0 {
			return c
		}
		return byID(b, a)
	})
	position := slices.Clone(ids)
	slices.SortFunc(position, byID)

	for _, c := range []struct {
		sort store.TaskSort
		want []uuid.UUID
	}{{store.TaskSortRecent, recent}, {store.TaskSortPosition, position}} {
		var got []uuid.UUID
		for _, offset := range []int{0, 3, 6} {
			page, err := e.store.ListTasks(ctx, store.TaskFilter{Statuses: []store.TaskStatus{store.TaskDone}, All: true,
				Sort: c.sort, Limit: 3, Offset: offset})
			if err != nil {
				t.Fatal(err)
			}
			for _, task := range page {
				got = append(got, task.ID)
			}
		}
		if !slices.Equal(got, c.want) {
			t.Fatalf("sort %q pages: %v, want %v", c.sort, got, c.want)
		}
	}
}

// CountTasks counts per status with every filter but Statuses, and always has all eight statuses.
func TestTaskCounts(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	def, _ := e.store.DefaultOrg(ctx)
	writer, err := e.store.CreateAgent(ctx, &store.Agent{OrgID: def.ID, Name: "Writer", Slug: "writer"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.store.CreateOrg(ctx, &store.Org{Name: "Other", Slug: "other"})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []store.NewTask{
		{Title: "alpha", Status: store.TaskBacklog, AssigneeAgentID: &writer.ID},
		{Title: "beta", Description: "alpha notes", Status: store.TaskTodo},
		{Title: "gamma", Status: store.TaskDone, AssigneeAgentID: &writer.ID},
		{Title: "old", Status: store.TaskDone},
		{Title: "alpha elsewhere", Status: store.TaskBacklog, OrgID: other.ID},
	} {
		if _, err := e.store.CreateTask(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.pool.Exec(ctx, "UPDATE tasks SET updated_at = NOW() - INTERVAL '30 days' WHERE title='old'"); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		f    store.TaskFilter
		want map[store.TaskStatus]int
	}{
		"statuses ignored": {store.TaskFilter{Statuses: []store.TaskStatus{store.TaskTodo}},
			map[store.TaskStatus]int{store.TaskBacklog: 1, store.TaskTodo: 1, store.TaskDone: 1}},
		"all":        {store.TaskFilter{All: true}, map[store.TaskStatus]int{store.TaskBacklog: 1, store.TaskTodo: 1, store.TaskDone: 2}},
		"query":      {store.TaskFilter{Query: "alpha"}, map[store.TaskStatus]int{store.TaskBacklog: 1, store.TaskTodo: 1}},
		"assignee":   {store.TaskFilter{AssigneeID: &writer.ID}, map[store.TaskStatus]int{store.TaskBacklog: 1, store.TaskDone: 1}},
		"unassigned": {store.TaskFilter{Unassigned: true, All: true}, map[store.TaskStatus]int{store.TaskTodo: 1, store.TaskDone: 1}},
	} {
		c.f.OrgID = &def.ID
		got, err := e.store.CountTasks(ctx, c.f)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(store.TaskStatuses) {
			t.Fatalf("%s: %d keys: %v", name, len(got), got)
		}
		for _, st := range store.TaskStatuses {
			if got[st] != c.want[st] {
				t.Fatalf("%s: %v, want %v", name, got, c.want)
			}
		}
	}
}

// GET /tasks pages with offset and sort=recent, and counts=1 adds total and per-status counts, so List
// mode reaches and bulk-manages rows past the old 500 cap. Without the new params the response is unchanged.
func TestTaskListAPI(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	def, _ := e.store.DefaultOrg(ctx)
	// 503 DONE tasks, task g finished g minutes ago; numbers far from the org's counter.
	if _, err := e.pool.Exec(ctx, `INSERT INTO tasks (org_id, number, title, status, completed_at)
		SELECT $1, 100000 + g, 'done ' || g, 'DONE', NOW() - g * INTERVAL '1 minute' FROM generate_series(1, 503) g`, def.ID); err != nil {
		t.Fatal(err)
	}
	type list struct {
		Items  []store.Task             `json:"items"`
		Total  int                      `json:"total"`
		Counts map[store.TaskStatus]int `json:"counts"`
	}
	const last = "/tasks?status=DONE&all=1&sort=recent&limit=100&offset=500&counts=1"
	var page list
	if code := e.do("GET", last, nil, &page); code != 200 {
		t.Fatalf("last page: %d", code)
	}
	if len(page.Items) != 3 || page.Items[0].Title != "done 501" || page.Items[2].Title != "done 503" ||
		page.Total != 503 || page.Counts[store.TaskDone] != 503 || len(page.Counts) != len(store.TaskStatuses) {
		t.Fatalf("last page: %d items, total %d, counts %v", len(page.Items), page.Total, page.Counts)
	}
	var ids []string
	for _, task := range page.Items {
		ids = append(ids, task.ID.String())
	}
	var res struct {
		OK []uuid.UUID `json:"ok"`
	}
	if code := e.do("POST", "/tasks/bulk", map[string]any{"action": "delete", "ids": ids}, &res); code != 200 || len(res.OK) != 3 {
		t.Fatalf("bulk delete: %d %v", code, res.OK)
	}
	page = list{}
	if e.do("GET", last, nil, &page); len(page.Items) != 0 || page.Total != 500 {
		t.Fatalf("after delete: %d items, total %d", len(page.Items), page.Total)
	}

	// No new params: exactly {"items"}, and limit is still capped at 500.
	var raw map[string]json.RawMessage
	if code := e.do("GET", "/tasks?all=1&limit=2000", nil, &raw); code != 200 || len(raw) != 1 || raw["items"] == nil {
		t.Fatalf("compat: %d %v", code, raw)
	}
	var items []store.Task
	if err := json.Unmarshal(raw["items"], &items); err != nil || len(items) != 500 {
		t.Fatalf("compat items: %d %v", len(items), err)
	}

	var bad struct {
		Error struct{ Code string } `json:"error"`
	}
	for _, q := range []string{"offset=-1", "sort=bogus"} {
		if code := e.do("GET", "/tasks?"+q, nil, &bad); code != 400 || bad.Error.Code != "invalid_argument" {
			t.Fatalf("%s: %d %+v", q, code, bad)
		}
	}
	page = list{}
	if code := e.do("GET", "/tasks?status=DONE&all=1&sort=recent&limit=1&offset=abc", nil, &page); code != 200 ||
		len(page.Items) != 1 || page.Items[0].Title != "done 1" {
		t.Fatalf("offset=abc: %d %+v", code, page.Items)
	}

	// q matches the task number as #N or N, and still the description.
	task, err := e.store.CreateTask(ctx, store.NewTask{Title: "needle", Description: "a haystack word"})
	if err != nil {
		t.Fatal(err)
	}
	// A number no seeded title contains.
	task.Number = 777777
	if _, err := e.pool.Exec(ctx, "UPDATE tasks SET number=$2 WHERE id=$1", task.ID, task.Number); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{fmt.Sprintf("%%23%d", task.Number), fmt.Sprint(task.Number), "haystack"} {
		page = list{}
		if e.do("GET", "/tasks?q="+q, nil, &page); len(page.Items) != 1 || page.Items[0].ID != task.ID {
			t.Fatalf("q=%s: %+v", q, page.Items)
		}
	}
}
