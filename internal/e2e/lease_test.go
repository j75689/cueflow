package e2e

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/mcpx"
	"github.com/j75689/cueflow/internal/store"
)

// claim claims the TODO tasks and returns task's claim.
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
	// The claim's run is the run the claim minted, so a handed-over run is resumed under its own claim.
	if _, err := e.store.StartAgentRun(ctx, first.RunID, task.ID, team["chief"].ID); err != nil {
		t.Fatal(err)
	}
	if got, err := e.store.GetAgentRun(ctx, first.RunID); err != nil || got.TaskID != task.ID {
		t.Fatalf("the claim's run: %+v %v", got, err)
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
	if second.RunID == first.RunID {
		t.Fatal("each claim needs its own run id")
	}

	if ok, err := e.store.ExtendTaskLease(ctx, task.ID, first.RunID, time.Minute); err != nil || ok {
		t.Fatalf("the lost claim extended the lease: %v %v", ok, err)
	}
	if ok, err := e.store.ExtendTaskLease(ctx, task.ID, second.RunID, time.Minute); err != nil || !ok {
		t.Fatalf("the current claim could not extend the lease: %v %v", ok, err)
	}
	if ok, err := e.store.SetClaimedTaskStatus(ctx, task.ID, first.RunID, store.TaskReview, nil); err != nil || ok {
		t.Fatalf("the lost claim finished the task: %v %v", ok, err)
	}
	if got, _ := e.store.GetTask(ctx, task.ID); got.Status != store.TaskInProgress {
		t.Fatalf("status %s", got.Status)
	}
	if ok, err := e.store.SetClaimedTaskStatus(ctx, task.ID, second.RunID, store.TaskReview, nil); err != nil || !ok {
		t.Fatalf("the current claim could not finish the task: %v %v", ok, err)
	}
}

// A run whose task was claimed again stops at its next tool call (or heartbeat) and leaves the task,
// and its thread, to the newer claim.
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
	if _, err := e.pool.Exec(ctx, `UPDATE tasks SET run_id = gen_random_uuid() WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	<-done

	got, _ := e.store.GetTask(ctx, task.ID)
	runs, _ := e.store.ListAgentRuns(ctx, task.ID)
	thread := threadText(t, e, task.ID.String())
	mu.Lock()
	defer mu.Unlock()
	if turns != 1 {
		t.Fatalf("the run should have stopped after its first turn, it took %d", turns)
	}
	// The turn's add_comment is refused before it runs: no call, no comment.
	if got.Status != store.TaskInProgress || got.Result != "" || len(runs) != 1 || runs[0].Status != "CANCELED" ||
		strings.Contains(thread, "halfway") || strings.Contains(thread, "tool_call:") ||
		strings.Contains(thread, "Done by the stalled run") || strings.Contains(thread, "Stopped.") {
		t.Fatalf("%s %q, runs %+v, thread:\n%s", got.Status, got.Result, runs, thread)
	}
}

// A task claimed by a version that recorded no run takes the run that resumes it as its claim, once.
func TestLegacyClaimIsAdoptedByItsRun(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Legacy", "assignee_agent_id": team["chief"].ID}, &task)
	cl := claim(t, e, task)
	if _, err := e.pool.Exec(ctx, `UPDATE tasks SET run_id = NULL WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.store.ExtendTaskLease(ctx, task.ID, cl.RunID, time.Minute); ok {
		t.Fatal("a claim with no run can't be extended before it is adopted")
	}
	if err := e.store.AdoptTaskRun(ctx, task.ID, cl.RunID); err != nil {
		t.Fatal(err)
	}
	other := uuid.New()
	if err := e.store.AdoptTaskRun(ctx, task.ID, other); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.store.ExtendTaskLease(ctx, task.ID, cl.RunID, time.Minute); err != nil || !ok {
		t.Fatalf("the adopting run: %v %v", ok, err)
	}
	if ok, _ := e.store.ExtendTaskLease(ctx, task.ID, other, time.Minute); ok {
		t.Fatal("a second adoption took over the claim")
	}
}

// A stalled turn's tool call is refused once the task was claimed again, before the heartbeat
// (2 minutes here) could notice: the tool never runs and the run stops.
func TestStalledRunsToolCallIsRefused(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	started, reclaimed := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	turns := 0
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		mu.Lock()
		turns++
		n := turns
		mu.Unlock()
		if n == 1 {
			close(started)
			<-reclaimed
			return calls("add_comment", map[string]any{"content": "halfway"})
		}
		return calls("complete_task", map[string]any{"summary": "Done by the stalled run."})
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Stalled tool", "assignee_agent_id": team["chief"].ID}, &task)
	done := make(chan struct{})
	go func() { defer close(done); e.crew.RunPending(ctx, 1) }()
	<-started
	if _, err := e.pool.Exec(ctx, `UPDATE tasks SET run_id = gen_random_uuid() WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	close(reclaimed)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the run kept going after its tool call found the claim lost")
	}

	runs, _ := e.store.ListAgentRuns(ctx, task.ID)
	thread := threadText(t, e, task.ID.String())
	mu.Lock()
	defer mu.Unlock()
	if turns != 1 || len(runs) != 1 || runs[0].Status != "CANCELED" || strings.Contains(thread, "halfway") || strings.Contains(thread, "tool_call:") {
		t.Fatalf("%d turns, runs %+v, thread:\n%s", turns, runs, thread)
	}
}

// The same through a run's MCP tool server, which runner and ACP agents call: a call made once the
// run lost the claim is refused, whichever instance serves it.
func TestMCPToolCallIsRefusedOnceTheClaimIsLost(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Remote", "assignee_agent_id": team["chief"].ID}, &task)
	cl := claim(t, e, task)
	token, err := e.store.CreateRunToken(ctx, task.ID, team["chief"].ID, &cl.RunID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcpx.Connect(ctx, mcpx.Options{Transport: mcpx.TransportStreamable,
		Endpoint: e.api.URL + crew.MCPPathPrefix + token, HTTPClient: &http.Client{}})
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	comment := func(text string) bool {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "add_comment", Arguments: map[string]any{"content": text}})
		if err != nil {
			t.Fatal(err)
		}
		return res.IsError
	}

	if comment("while holding the claim") {
		t.Fatal("the claim's run was refused")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE tasks SET run_id = gen_random_uuid() WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if !comment("after losing it") {
		t.Fatal("a run that lost the claim was let through")
	}
	if thread := threadText(t, e, task.ID.String()); !strings.Contains(thread, "while holding the claim") || strings.Contains(thread, "after losing it") {
		t.Fatalf("thread:\n%s", thread)
	}
}

// A runner run whose claim was lost by the time its delivery hands it over ends there: the job is
// stopped and the run is not handed over.
func TestLostClaimIsNotHandedOver(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := &fakeQueue{}
	e.crew.Queue = q
	agent := slowRunnerAgent(ctx, t, e)
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Write about ACP", "assignee_agent_id": agent.ID}, &task)

	e.crew.DeliveryLimit = 3 * time.Second // hands over at 2.8 s; the agent makes no call before 5 s
	go func() {
		time.Sleep(time.Second)
		_, _ = e.pool.Exec(context.Background(), `UPDATE tasks SET run_id = gen_random_uuid() WHERE id=$1`, task.ID)
	}()
	if err := e.crew.HandleCrew(ctx); err != nil {
		t.Fatal(err)
	}
	runs, _ := e.store.ListAgentRuns(ctx, task.ID)
	var jobs_ []store.RunnerJob
	e.do("GET", "/tasks/"+task.ID.String()+"/runner-jobs", nil, &jobs_)
	if len(runs) != 1 || runs[0].Status != "CANCELED" || runs[0].Outcome == "handover" ||
		len(jobs_) != 1 || !store.JobFinished(jobs_[0].Status) {
		t.Fatalf("runs %+v, jobs %+v", runs, jobs_)
	}
}
