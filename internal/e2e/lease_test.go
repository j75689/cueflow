package e2e

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/j75689/cueflow/internal/runnerapi"
	"github.com/j75689/cueflow/internal/store"
)

// claim claims the TODO tasks and returns task's claim.
func claim(t *testing.T, e *env, task store.Task) store.Claim {
	t.Helper()
	claims, err := e.store.ClaimTodo(context.Background(), time.Minute, 10, 3)
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
// run may no longer act on the task (claimed again, or stopped by a human) is refused, whichever
// instance serves it, and ends the run: its outcome is recorded on the token for the instance that
// owns the run, and later calls are told the run is over.
func TestMCPToolCallIsRefusedOnceTheClaimIsLost(t *testing.T) {
	for _, tc := range []struct{ name, lose string }{
		{"claimed again", `UPDATE tasks SET run_id = gen_random_uuid() WHERE id=$1`},
		{"stopped by a human", `UPDATE tasks SET status = 'CANCELED' WHERE id=$1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			comment := func(text string) (string, bool) {
				res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "add_comment", Arguments: map[string]any{"content": text}})
				if err != nil {
					t.Fatal(err)
				}
				return res.Content[0].(*mcp.TextContent).Text, res.IsError
			}

			if _, refused := comment("while holding the claim"); refused {
				t.Fatal("the claim's run was refused")
			}
			if _, err := e.pool.Exec(ctx, tc.lose, task.ID); err != nil {
				t.Fatal(err)
			}
			if _, refused := comment("after losing it"); !refused {
				t.Fatal("a run that lost the claim was let through")
			}
			rt, err := e.store.RunTokenByPlain(ctx, token)
			if err != nil || !strings.Contains(string(rt.Outcome), `"canceled"`) {
				t.Fatalf("the refusal should end the run on its token: %s %v", rt.Outcome, err)
			}
			if text, refused := comment("and again"); !refused || !strings.Contains(text, "already finished") {
				t.Fatalf("a call after the refusal: %q", text)
			}
			if thread := threadText(t, e, task.ID.String()); !strings.Contains(thread, "while holding the claim") ||
				strings.Contains(thread, "after losing it") || strings.Contains(thread, "and again") {
				t.Fatalf("thread:\n%s", thread)
			}
		})
	}
}

// A runner run whose claim was lost by the time its delivery hands it over ends there: the job is
// stopped and the run is not handed over. No runner takes the job, so nothing but the hand-over (the
// lease is 2 minutes) can end the run before the delivery's time is up.
func TestLostClaimIsNotHandedOver(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	q := &fakeQueue{}
	e.crew.Queue = q
	agent, _ := runnerAgent(t, e)
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Write about ACP", "assignee_agent_id": agent.ID}, &task)

	e.crew.DeliveryLimit = 3 * time.Second // hands over at 2.8 s
	go func() {
		time.Sleep(time.Second)
		_, _ = e.pool.Exec(context.Background(), `UPDATE tasks SET run_id = gen_random_uuid() WHERE id=$1`, task.ID)
	}()
	start := time.Now()
	if err := e.crew.HandleCrew(ctx); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	runs, _ := e.store.ListAgentRuns(ctx, task.ID)
	var jobs_ []store.RunnerJob
	e.do("GET", "/tasks/"+task.ID.String()+"/runner-jobs", nil, &jobs_)
	if took < 2700*time.Millisecond || len(runs) != 1 || runs[0].Status != "CANCELED" || runs[0].Outcome == "handover" ||
		len(jobs_) != 1 || jobs_[0].Status != store.JobCanceled {
		t.Fatalf("after %s: runs %+v, jobs %+v", took, runs, jobs_)
	}
}

// A turn that comes back after its task was stopped or claimed again is stale: its reply is not
// written (here a plain-text answer, which would otherwise complete the task with it) and the run
// ends canceled.
func TestStalledTurnsReplyIsNotWritten(t *testing.T) {
	for _, tc := range []struct{ name, lose string }{
		{"claimed again", `UPDATE tasks SET run_id = gen_random_uuid() WHERE id=$1`},
		{"stopped by a human", `UPDATE tasks SET status = 'CANCELED' WHERE id=$1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, team := crewSetup(t)
			ctx := context.Background()
			started, lost := make(chan struct{}), make(chan struct{})
			var mu sync.Mutex
			turns := 0
			setScript(t, func(t *testing.T, req llm.Request) llm.Message {
				mu.Lock()
				turns++
				mu.Unlock()
				close(started)
				<-lost
				return llm.Message{Role: llm.RoleAssistant, Content: "The stale summary."}
			})
			var task store.Task
			e.do("POST", "/tasks", map[string]any{"title": "Stalled reply", "assignee_agent_id": team["chief"].ID}, &task)
			done := make(chan struct{})
			go func() { defer close(done); e.crew.RunPending(ctx, 1) }()
			<-started
			if _, err := e.pool.Exec(ctx, tc.lose, task.ID); err != nil {
				t.Fatal(err)
			}
			close(lost)
			<-done

			got, _ := e.store.GetTask(ctx, task.ID)
			runs, _ := e.store.ListAgentRuns(ctx, task.ID)
			thread := threadText(t, e, task.ID.String())
			mu.Lock()
			defer mu.Unlock()
			if turns != 1 || got.Result != "" || len(runs) != 1 || runs[0].Status != "CANCELED" || strings.Contains(thread, "stale summary") {
				t.Fatalf("%d turns, %s %q, runs %+v, thread:\n%s", turns, got.Status, got.Result, runs, thread)
			}
		})
	}
}

// A runner agent's tool call refused on the instance that owns the run (the task was claimed again
// while the agent worked) ends the run and stops the runner job, instead of leaving the agent to run
// into its timeout.
func TestRefusedRunnerCallStopsTheJob(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent := slowRunnerAgent(ctx, t, e) // makes its first tool call after 5 s
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Write about ACP", "assignee_agent_id": agent.ID}, &task)
	go func() {
		time.Sleep(2 * time.Second)
		_, _ = e.pool.Exec(context.Background(), `UPDATE tasks SET run_id = gen_random_uuid() WHERE id=$1`, task.ID)
	}()
	done := make(chan struct{})
	go func() { defer close(done); e.crew.RunPending(ctx, 1) }()
	select {
	case <-done:
	case <-time.After(40 * time.Second):
		t.Fatal("the run kept going after its tool call was refused")
	}

	runs, _ := e.store.ListAgentRuns(ctx, task.ID)
	var jobs_ []store.RunnerJob
	e.do("GET", "/tasks/"+task.ID.String()+"/runner-jobs", nil, &jobs_)
	if len(runs) != 1 || runs[0].Status != "CANCELED" || len(jobs_) != 1 || !store.JobFinished(jobs_[0].Status) {
		t.Fatalf("runs %+v, jobs %+v", runs, jobs_)
	}
	if thread := threadText(t, e, task.ID.String()); strings.Contains(thread, "Done via ACP") {
		t.Fatalf("the refused agent's work reached the thread:\n%s", thread)
	}
}

// A runner whose run lost its task is told to stop by its own events and heartbeats, so it stops
// even when no instance watches the run any more, and what it reports is not written.
func TestLostRunnerJobIsStoppedByTheServer(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent, cfg := runnerAgent(t, e) // this test plays the runner itself
	post := func(path string, in, out any) {
		t.Helper()
		raw, _ := json.Marshal(in)
		req, _ := http.NewRequest(http.MethodPost, cfg.URL+runnerapi.BasePath+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent {
			return // a claim with nothing queued yet
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Write about ACP", "assignee_agent_id": agent.ID}, &task)
	go e.crew.RunPending(ctx, 1) // queues the job and waits on it until ctx ends

	var job runnerapi.Job
	wait := 0
	for deadline := time.Now().Add(10 * time.Second); job.ID == "" && time.Now().Before(deadline); {
		post("/jobs/claim", runnerapi.ClaimRequest{Protocol: runnerapi.ProtocolVersion, Wait: &wait}, &job)
		time.Sleep(100 * time.Millisecond)
	}
	if job.ID == "" {
		t.Fatal("no job was queued")
	}
	say := func(text string) bool {
		var st runnerapi.JobStatus
		post("/jobs/"+job.ID+"/events", runnerapi.EventsRequest{Events: []runnerapi.Event{{Kind: "message", Content: text}}}, &st)
		return st.Cancel
	}
	if say("while holding the claim") {
		t.Fatal("the run's runner was told to stop")
	}
	// A claim made by the version before claims recorded their run holds for its runner, which must
	// keep working while that version is replaced.
	if _, err := e.pool.Exec(ctx, `UPDATE tasks SET run_id = NULL WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	var legacy runnerapi.JobStatus
	post("/jobs/"+job.ID+"/heartbeat", struct{}{}, &legacy)
	if legacy.Cancel || say("under the old claim") {
		t.Fatal("a claim with no run recorded stopped its runner")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE tasks SET run_id = gen_random_uuid() WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	var beat runnerapi.JobStatus
	post("/jobs/"+job.ID+"/heartbeat", struct{}{}, &beat)
	if !beat.Cancel || !say("after losing it") {
		t.Fatalf("the runner of a lost run should be told to stop: heartbeat %+v", beat)
	}
	var jobs_ []store.RunnerJob
	e.do("GET", "/tasks/"+task.ID.String()+"/runner-jobs", nil, &jobs_)
	if len(jobs_) != 1 || jobs_[0].Status != store.JobCanceling {
		t.Fatalf("jobs %+v", jobs_)
	}
	if thread := threadText(t, e, task.ID.String()); !strings.Contains(thread, "while holding the claim") || !strings.Contains(thread, "under the old claim") ||
		strings.Contains(thread, "after losing it") {
		t.Fatalf("thread:\n%s", thread)
	}
}
