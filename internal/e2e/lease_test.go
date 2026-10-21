package e2e

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

// claim claims TODO tasks until it gets id's claim.
func claim(t *testing.T, e *env, task store.Task) store.Claim {
	t.Helper()
	claims, err := e.store.ClaimTodo(context.Background(), time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, cl := range claims {
		if cl.ID == task.ID {
			return cl
		}
	}
	t.Fatalf("task %s was not claimed: %+v", task.ID, claims)
	return store.Claim{}
}

// A claim whose holder stalled past its lease is lost once the task is claimed again: the old
// holder can neither extend the lease nor finish the task, the new one can.
func TestLeaseIsFencedByClaim(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Fenced", "assignee_agent_id": team["chief"].ID}, &task)

	first := claim(t, e, task)
	// A handed-over run is resumed under the claim it was started with, whatever the task says by then.
	run, err := e.store.StartAgentRun(ctx, task.ID, team["chief"].ID, first.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := e.store.GetAgentRun(ctx, run.ID); err != nil || got.Attempt != first.Attempt {
		t.Fatalf("the run's claim: %+v %v", got, err)
	}
	// The holder stalls: the lease runs out, the sweep interrupts the task and a human reruns it.
	if _, err := e.pool.Exec(ctx, `UPDATE tasks SET lease_until = NOW() - interval '1 second' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	for _, step := range [][2]store.TaskStatus{{store.TaskInProgress, store.TaskWaitingHuman}, {store.TaskWaitingHuman, store.TaskTodo}} {
		if ok, err := e.store.SetTaskStatusIf(ctx, task.ID, []store.TaskStatus{step[0]}, step[1], nil); err != nil || !ok {
			t.Fatalf("%s -> %s: %v %v", step[0], step[1], ok, err)
		}
	}
	second := claim(t, e, task)
	if second.Attempt != first.Attempt+1 {
		t.Fatalf("attempts %d then %d", first.Attempt, second.Attempt)
	}

	if ok, err := e.store.ExtendTaskLease(ctx, task.ID, first.Attempt, time.Minute); err != nil || ok {
		t.Fatalf("the lost claim extended the lease: %v %v", ok, err)
	}
	if ok, err := e.store.ExtendTaskLease(ctx, task.ID, second.Attempt, time.Minute); err != nil || !ok {
		t.Fatalf("the current claim could not extend the lease: %v %v", ok, err)
	}
	if ok, err := e.store.SetClaimedTaskStatus(ctx, task.ID, first.Attempt, store.TaskReview, nil); err != nil || ok {
		t.Fatalf("the lost claim finished the task: %v %v", ok, err)
	}
	if got, _ := e.store.GetTask(ctx, task.ID); got.Status != store.TaskInProgress {
		t.Fatalf("status %s", got.Status)
	}
	if ok, err := e.store.SetClaimedTaskStatus(ctx, task.ID, second.Attempt, store.TaskReview, nil); err != nil || !ok {
		t.Fatalf("the current claim could not finish the task: %v %v", ok, err)
	}
}

// A run whose task was claimed again stops at its next heartbeat and leaves the task, and its
// thread, to the newer claim.
func TestStalledRunLosesAReclaimedTask(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	e.crew.Lease = 300 * time.Millisecond // heartbeat every 100 ms
	started := make(chan struct{})
	var mu sync.Mutex
	turns := 0
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		mu.Lock()
		turns++
		n := turns
		mu.Unlock()
		if n == 1 {
			close(started)
			time.Sleep(time.Second) // stalled; the task is claimed again meanwhile
			return calls("add_comment", map[string]any{"content": "halfway"})
		}
		return calls("complete_task", map[string]any{"summary": "Done by the stalled run."})
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Stalled", "assignee_agent_id": team["chief"].ID}, &task)
	done := make(chan struct{})
	go func() { defer close(done); e.crew.RunPending(ctx, 1) }()
	<-started

	// Another instance claimed the task after this run's lease ran out (as TestLeaseIsFencedByClaim).
	if _, err := e.pool.Exec(ctx, `UPDATE tasks SET attempts = attempts + 1 WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	<-done

	got, _ := e.store.GetTask(ctx, task.ID)
	runs, _ := e.store.ListAgentRuns(ctx, task.ID)
	thread := threadText(t, e, task.ID.String())
	mu.Lock()
	defer mu.Unlock()
	if turns != 1 {
		t.Fatalf("the heartbeat should have stopped the run after its first turn, it took %d", turns)
	}
	if got.Status != store.TaskInProgress || got.Result != "" || len(runs) != 1 || runs[0].Status != "CANCELED" ||
		strings.Contains(thread, "Done by the stalled run") || strings.Contains(thread, "Stopped.") {
		t.Fatalf("%s %q, runs %+v, thread:\n%s", got.Status, got.Result, runs, thread)
	}
}
