package e2e

import (
	"context"
	"testing"

	"github.com/j75689/cueflow/internal/store"
)

func TestOrgsIsolateBoards(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	var orgs []store.Org
	e.do("GET", "/orgs", nil, &orgs)
	if len(orgs) != 1 || orgs[0].Name != "Default" || orgs[0].Slug != "default" {
		t.Fatalf("default org: %+v", orgs)
	}
	def := orgs[0]

	var renamed store.Org
	if code := e.do("PATCH", "/orgs/"+def.ID.String(), map[string]any{"name": "FutureIndex"}, &renamed); code != 200 || renamed.Name != "FutureIndex" {
		t.Fatalf("rename: %d %+v", code, renamed)
	}

	var dev store.Org
	if code := e.do("POST", "/orgs", map[string]any{"name": "CueFlow Dev", "seed_agents": true}, &dev); code != 201 || dev.Slug != "cueflow-dev" {
		t.Fatalf("create org: %d %+v", code, dev)
	}

	// Same slug in two orgs is fine; agents only show up in their own org.
	var a1, a2 store.Agent
	if code := e.do("POST", "/agents", map[string]any{"name": "Coder", "slug": "coder"}, &a1); code != 201 {
		t.Fatalf("agent in default org: %d", code)
	}
	if code := e.do("POST", "/agents?org=cueflow-dev", map[string]any{"name": "Coder", "slug": "coder"}, &a2); code != 201 {
		t.Fatalf("agent in dev org: %d", code)
	}
	if a1.OrgID != def.ID || a2.OrgID != dev.ID {
		t.Fatalf("agent orgs: %s %s", a1.OrgID, a2.OrgID)
	}
	var devAgents []store.Agent
	e.do("GET", "/agents?org="+dev.ID.String(), nil, &devAgents)
	if len(devAgents) != 2 { // the seeded Chief + Coder
		t.Fatalf("dev agents: %d", len(devAgents))
	}

	// Task numbers count per org.
	var t1, t2, t3 store.Task
	e.do("POST", "/tasks", map[string]any{"title": "default task", "status": "BACKLOG"}, &t1)
	e.do("POST", "/tasks?org=cueflow-dev", map[string]any{"title": "dev task 1", "status": "BACKLOG"}, &t2)
	e.do("POST", "/tasks?org=cueflow-dev", map[string]any{"title": "dev task 2", "status": "BACKLOG"}, &t3)
	if t1.Number != 1 || t2.Number != 1 || t3.Number != 2 || t2.OrgID != dev.ID {
		t.Fatalf("numbers: %d %d %d", t1.Number, t2.Number, t3.Number)
	}
	var list struct{ Items []store.Task }
	e.do("GET", "/tasks?org=cueflow-dev&all=1", nil, &list)
	if len(list.Items) != 2 {
		t.Fatalf("dev board: %d tasks", len(list.Items))
	}

	// A subtask follows its parent's org even when the request names another.
	var sub store.Task
	if code := e.do("POST", "/tasks", map[string]any{"title": "sub", "parent_id": t2.ID, "status": "BACKLOG"}, &sub); code != 201 || sub.OrgID != dev.ID || sub.Number != 3 {
		t.Fatalf("subtask: %d %+v", code, sub)
	}

	// Cross-org assignment is refused.
	if code := e.do("POST", "/tasks?org=cueflow-dev", map[string]any{"title": "x", "assignee_agent_id": a1.ID}, nil); code != 400 {
		t.Fatalf("cross-org assignee on create: %d", code)
	}
	if code := e.do("PATCH", "/tasks/"+t2.ID.String(), map[string]any{"assignee_agent_id": a1.ID}, nil); code != 400 {
		t.Fatalf("cross-org assignee on update: %d", code)
	}
	if code := e.do("POST", "/schedules?org=cueflow-dev", map[string]any{"name": "s", "cron_expr": "0 9 * * *",
		"task_title": "s", "assignee_agent_id": a1.ID}, nil); code != 400 {
		t.Fatalf("cross-org schedule assignee: %d", code)
	}

	// Posts are reviewed per org.
	var post struct{ ID string }
	e.do("POST", "/posts?org=cueflow-dev", map[string]any{"title": "p", "content": "hello", "platforms": []string{"threads"}}, &post)
	var posts struct{ Total int }
	e.do("GET", "/posts", nil, &posts)
	if posts.Total != 0 {
		t.Fatalf("default org sees %d posts", posts.Total)
	}
	e.do("GET", "/posts?org=cueflow-dev", nil, &posts)
	if posts.Total != 1 {
		t.Fatalf("dev org sees %d posts", posts.Total)
	}

	// A non-empty org can't be deleted; unknown orgs are rejected.
	if code := e.do("DELETE", "/orgs/"+dev.ID.String(), nil, nil); code != 400 {
		t.Fatalf("delete non-empty org: %d", code)
	}
	if code := e.do("GET", "/tasks?org=nope", nil, nil); code != 400 {
		t.Fatalf("unknown org: %d", code)
	}
	var empty store.Org
	e.do("POST", "/orgs", map[string]any{"name": "Scratch"}, &empty)
	if code := e.do("DELETE", "/orgs/"+empty.ID.String(), nil, nil); code != 204 {
		t.Fatalf("delete empty org: %d", code)
	}

	if _, err := e.store.GetTaskByNumber(ctx, dev.ID, 2); err != nil {
		t.Fatalf("task #2 in dev org: %v", err)
	}
}
