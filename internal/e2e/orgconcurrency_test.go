package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/store"
)

// An org's agent_concurrency (CUEFLOW_AGENT_CONCURRENCY when unset) caps the tasks the crew works for it,
// counted across every claimer: claimers racing from several instances never claim past it, a full org
// does not hold up another, and a self-driven (external) agent's tasks take no slot.
func TestOrgAgentConcurrency(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	const orgDefault = 2
	var meta map[string]any
	e.do("GET", "/meta", nil, &meta)
	if meta["agent_concurrency"] != float64(3) { // the crew's default; main.go sets CUEFLOW_AGENT_CONCURRENCY
		t.Fatalf("meta agent_concurrency = %v", meta["agent_concurrency"])
	}

	a, _ := e.store.DefaultOrg(ctx)
	var b store.Org
	if code := e.do("POST", "/orgs", map[string]any{"name": "Other"}, &b); code != 201 {
		t.Fatalf("create org: %d", code)
	}
	for _, bad := range []any{0, store.MaxOrgAgentConcurrency + 1, "two", 1.5} {
		if code := e.do("PATCH", "/orgs/"+b.ID.String(), map[string]any{"agent_concurrency": bad}, nil); code != 400 {
			t.Fatalf("agent_concurrency %v: %d, want 400", bad, code)
		}
	}
	if code := e.do("PATCH", "/orgs/"+b.ID.String(), map[string]any{"agent_concurrency": 1}, &b); code != 200 || b.AgentConcurrency == nil || *b.AgentConcurrency != 1 {
		t.Fatalf("set agent_concurrency: %d %+v", code, b.AgentConcurrency)
	}
	if code := e.do("PATCH", "/orgs/"+b.ID.String(), map[string]any{"name": "Other org"}, &b); code != 200 || b.AgentConcurrency == nil {
		t.Fatalf("a PATCH without agent_concurrency must keep it: %d %+v", code, b.AgentConcurrency)
	}

	tasks := func(org, slug string, runtime *uuid.UUID, n int) []uuid.UUID {
		t.Helper()
		var agent store.Agent
		body := map[string]any{"name": slug, "slug": slug, "capabilities": []string{"post_deliverables"}}
		if runtime != nil {
			body["runtime_connector_id"] = *runtime
		}
		if code := e.do("POST", "/agents?org="+org, body, &agent); code != 201 {
			t.Fatalf("create agent %s: %d", slug, code)
		}
		var ids []uuid.UUID
		for i := range n {
			var task store.Task
			if code := e.do("POST", "/tasks?org="+org, map[string]any{"title": fmt.Sprintf("%s %d", slug, i), "assignee_agent_id": agent.ID}, &task); code != 201 || task.Status != store.TaskTodo {
				t.Fatalf("create task: %d %s", code, task.Status)
			}
			ids = append(ids, task.ID)
		}
		return ids
	}
	// Two of org A's external agent's tasks are being worked by that agent: they must not count.
	var ext store.Connector
	e.do("POST", "/connectors", map[string]any{"name": "Self-driven", "type": "external", "config": map[string]any{}}, &ext)
	for _, id := range tasks(a.Slug, "outside", &ext.ID, 2) {
		if _, err := e.pool.Exec(ctx, `UPDATE tasks SET status='IN_PROGRESS', lease_until=NOW() + interval '1 hour' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	inA := tasks(a.Slug, "writer-a", nil, 5)
	inB := tasks(b.Slug, "writer-b", nil, 3)
	orgOf := map[uuid.UUID]string{}
	for _, id := range inA {
		orgOf[id] = "a"
	}
	for _, id := range inB {
		orgOf[id] = "b"
	}

	// Eight claimers at once, as if from eight instances.
	claimAll := func() map[string]int {
		t.Helper()
		var mu sync.Mutex
		got := map[string]int{}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				claims, err := e.store.ClaimTodo(ctx, time.Minute, 10, orgDefault)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				for _, cl := range claims {
					got[orgOf[cl.ID]]++
				}
			}()
		}
		close(start)
		wg.Wait()
		return got
	}
	if got := claimAll(); got["a"] != orgDefault || got["b"] != 1 || len(got) != 2 {
		t.Fatalf("first claims: %v, want a=%d (the default) and b=1 (its own)", got, orgDefault)
	}
	if got := claimAll(); len(got) != 0 {
		t.Fatalf("full orgs got more: %v", got)
	}

	// Clearing B's limit puts it on the default; A, still full, gets nothing more.
	if code := e.do("PATCH", "/orgs/"+b.ID.String(), map[string]any{"agent_concurrency": nil}, &b); code != 200 || b.AgentConcurrency != nil {
		t.Fatalf("clear agent_concurrency: %d %+v", code, b.AgentConcurrency)
	}
	if got := claimAll(); got["b"] != orgDefault-1 || len(got) != 1 {
		t.Fatalf("after clearing B's limit: %v, want b=%d", got, orgDefault-1)
	}
	// A task of A that ends frees one slot for A alone.
	if _, err := e.pool.Exec(ctx, `UPDATE tasks SET status='REVIEW' WHERE status='IN_PROGRESS' AND id = ANY($1)
		AND id = (SELECT id FROM tasks WHERE status='IN_PROGRESS' AND id = ANY($1) LIMIT 1)`, inA); err != nil {
		t.Fatal(err)
	}
	if got := claimAll(); got["a"] != 1 || len(got) != 1 {
		t.Fatalf("after one of A's tasks ended: %v, want a=1", got)
	}
}

// Two claimers racing for a full org, in the order that overshoots unless claimers take turns per org:
// the first has claimed one task and waits on the second (a row another writer holds), an urgent task
// arrives, and the second claimer, which cannot see the first's uncommitted claims, would pick the
// urgent one.
func TestOrgAgentConcurrencyClaimersTakeTurns(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	org, _ := e.store.DefaultOrg(ctx)
	var agent store.Agent
	e.do("POST", "/agents", map[string]any{"name": "Writer", "slug": "writer", "capabilities": []string{"post_deliverables"}}, &agent)
	newTask := func(title, priority string) uuid.UUID {
		var task store.Task
		if code := e.do("POST", "/tasks", map[string]any{"title": title, "priority": priority, "assignee_agent_id": agent.ID}, &task); code != 201 {
			t.Fatalf("create task: %d", code)
		}
		return task.ID
	}
	newTask("first", "normal")
	second := newTask("second", "normal")

	holder, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if _, err := holder.Exec(ctx, `SELECT 1 FROM tasks WHERE id=$1 FOR UPDATE`, second); err != nil {
		t.Fatal(err)
	}
	waiting := func(n int) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			var got int
			if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
				WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got >= n {
				return
			}
		}
		t.Fatalf("%d claimers never waited", n)
	}
	var wg sync.WaitGroup
	claimed := make([]int, 2)
	claimer := func(i int) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claims, err := e.store.ClaimTodo(ctx, time.Minute, 10, 2)
			if err != nil {
				t.Error(err)
			}
			claimed[i] = len(claims)
		}()
	}
	claimer(0)
	waiting(1) // the first claimer waits on the second task
	newTask("urgent", "urgent")
	claimer(1)
	waiting(2) // the second waits too
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	var running int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE org_id=$1 AND status='IN_PROGRESS'`, org.ID).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if running != 2 {
		t.Fatalf("%d tasks running (claimers got %v), want the org's 2", running, claimed)
	}
}
