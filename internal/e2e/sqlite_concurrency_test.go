package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/store"
	storesqlite "github.com/j75689/cueflow/internal/store/sqlite"
)

// On SQLite every write and claim takes turns on one connection. Many goroutines writing messages and
// claiming tasks at once must all succeed (no SQLITE_BUSY reaches them), each claim must be the
// task's only one, and every message must be stored.
func TestSQLiteConcurrentWritesAndClaims(t *testing.T) {
	e := setup(t)
	if _, ok := e.db.(*storesqlite.TestHooks); !ok {
		t.Skip("tests SQLite's single writer")
	}
	ctx := context.Background()
	def, _ := e.store.DefaultOrg(ctx)
	agent, err := e.store.CreateAgent(ctx, &store.Agent{OrgID: def.ID, Name: "Worker", IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	var tasks []uuid.UUID
	for i := range 20 {
		task, err := e.store.CreateTask(ctx, store.NewTask{OrgID: def.ID, Title: fmt.Sprint("task ", i), AssigneeAgentID: &agent.ID})
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task.ID)
	}

	const workers = 50
	deadline := time.Now().Add(5 * time.Second)
	var (
		mu       sync.Mutex
		claimed  = map[uuid.UUID]bool{}
		messages int
		claims   int
		errs     []error
	)
	fail := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for time.Now().Before(deadline) {
				if w%2 == 0 {
					id := tasks[rand.IntN(len(tasks))]
					if _, err := e.store.AddTaskMessage(ctx, id, "human", nil, "message", fmt.Sprint("from ", w), nil); err != nil {
						fail(fmt.Errorf("add message: %w", err))
						return
					}
					mu.Lock()
					messages++
					mu.Unlock()
					continue
				}
				got, err := e.store.ClaimTodo(ctx, time.Minute, 1, workers)
				if err != nil {
					fail(fmt.Errorf("claim: %w", err))
					return
				}
				for _, c := range got {
					mu.Lock()
					twice := claimed[c.ID]
					claimed[c.ID] = true
					claims++
					mu.Unlock()
					if twice {
						fail(fmt.Errorf("task %s claimed while claimed", c.ID))
						return
					}
					if _, err := e.store.TaskMessages(ctx, c.ID); err != nil {
						fail(fmt.Errorf("read: %w", err))
						return
					}
					mu.Lock()
					delete(claimed, c.ID)
					mu.Unlock()
					if ok, err := e.store.RequeueClaimedTask(ctx, c.ID, c.RunID, 0); err != nil || !ok {
						fail(fmt.Errorf("requeue %s: %v %v", c.ID, ok, err))
						return
					}
				}
			}
		})
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("%d error(s), first: %v", len(errs), errs[0])
	}
	n, err := e.db.Count(ctx, "task_messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != messages || messages == 0 || claims == 0 {
		t.Fatalf("stored %d messages of %d written; %d claims", n, messages, claims)
	}
	t.Logf("%d messages, %d claims", messages, claims)
}
