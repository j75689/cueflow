package e2e

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/api"
	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/jobs"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

// fakeQueue records what would have been sent to Cloud Tasks.
type fakeQueue struct {
	mu  sync.Mutex
	got []jobs.Kind
}

func (q *fakeQueue) Enqueue(_ context.Context, kind jobs.Kind, _ uuid.UUID, _ time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.got = append(q.got, kind)
	return nil
}

func (q *fakeQueue) count(kind jobs.Kind) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, k := range q.got {
		if k == kind {
			n++
		}
	}
	return n
}

// With a queue, creating work enqueues a delivery instead of waking an in-process loop, and the
// handler that delivery reaches does the work; a duplicate delivery finds nothing to do.
func TestCloudQueueDeliversCrewWork(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	q := &fakeQueue{}
	e.crew.Queue = q
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		return calls("complete_task", map[string]any{"summary": "Done in the cloud."})
	})

	var task store.Task
	if code := e.do("POST", "/tasks", map[string]any{"title": "Cloud task", "assignee_agent_id": team["chief"].ID}, &task); code != 201 {
		t.Fatalf("create task: %d", code)
	}
	if q.count(jobs.KindCrew) == 0 {
		t.Fatal("assigning a task must enqueue a crew delivery")
	}
	if got, _ := e.store.GetTask(ctx, task.ID); got.Status != store.TaskTodo {
		t.Fatalf("nothing should run before the delivery arrives, task is %s", got.Status)
	}

	if err := e.crew.HandleCrew(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskReview || got.Result != "Done in the cloud." {
		t.Fatalf("task after delivery: %s %q", got.Status, got.Result)
	}
	if err := e.crew.HandleCrew(ctx); err != nil {
		t.Fatalf("a duplicate delivery must be harmless: %v", err)
	}
	if got, _ := e.store.GetTask(ctx, task.ID); got.Status != store.TaskReview {
		t.Fatalf("duplicate delivery changed the task to %s", got.Status)
	}
}

// The external clock fires each due schedule exactly once, however many instances tick at once,
// and never fires one that was only just created or edited, nor one that was missed long ago.
func TestExternalClockFiresOnce(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	var sc store.Schedule
	if code := e.do("POST", "/schedules", map[string]any{"name": "Daily", "cron_expr": "0 9 * * *", "timezone": "UTC",
		"assignee_agent_id": team["chief"].ID, "task_title": "Daily job"}, &sc); code != 201 {
		t.Fatalf("create schedule: %d", code)
	}
	tasksOf := func() int {
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FROM tasks WHERE schedule_id=$1`, sc.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	nextRun := func() *time.Time {
		var at *time.Time
		if err := e.pool.QueryRow(ctx, `SELECT next_run_at FROM schedules WHERE id=$1`, sc.ID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}

	if err := e.sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if tasksOf() != 0 || nextRun() == nil {
		t.Fatalf("first tick only schedules: tasks=%d next=%v", tasksOf(), nextRun())
	}

	due := func(ago string) {
		if _, err := e.pool.Exec(ctx, `UPDATE schedules SET next_run_at = NOW() - $1::interval WHERE id=$2`, ago, sc.ID); err != nil {
			t.Fatal(err)
		}
	}
	due("1 minute")
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.sched.Tick(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := tasksOf(); n != 1 {
		t.Fatalf("four concurrent ticks must fire the activation once, fired %d", n)
	}
	if at := nextRun(); at == nil || !at.After(time.Now()) {
		t.Fatalf("next activation must move into the future: %v", at)
	}

	due("3 hours") // the clock was down: skipped, not run at a surprising hour
	if err := e.sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if n := tasksOf(); n != 1 {
		t.Fatalf("a long-missed activation must be skipped, tasks=%d", n)
	}
	if at := nextRun(); at == nil || !at.After(time.Now()) {
		t.Fatalf("a skipped activation still moves on: %v", at)
	}

	// Editing the schedule makes the clock recompute it instead of trusting the old time.
	if code := e.do("PATCH", "/schedules/"+sc.ID.String(), map[string]any{"cron_expr": "30 9 * * *"}, nil); code != 200 {
		t.Fatalf("patch: %d", code)
	}
	if nextRun() != nil {
		t.Fatal("editing a schedule must clear next_run_at")
	}
}

// /internal/* only exists in the cloud, requires the auth middleware, and routes deliveries to
// handlers: success is 204, a failing handler is 500 (so the queue retries), an unknown kind 404.
func TestInternalWorkRoutes(t *testing.T) {
	e := setup(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var calls, ticks int
	var failNext bool
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer good" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	srv := httptest.NewServer((&api.Server{Store: e.store, Hub: hub.New(e.store, logger), Broker: events.NewBroker(),
		Scheduler: e.sched, Crew: e.crew, Logger: logger,
		InternalAuth: auth,
		Handlers: jobs.Handlers{jobs.KindCrew: func(context.Context, uuid.UUID) error {
			calls++
			if failNext {
				return io.ErrUnexpectedEOF
			}
			return nil
		}},
		Tick: func(context.Context) error { ticks++; return nil },
	}).Handler())
	defer srv.Close()

	post := func(path, token string) int {
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(""))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	id := uuid.New()
	work := jobs.WorkPath(jobs.KindCrew, id)
	if c := post(work, ""); c != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("unauthenticated delivery: %d, handler calls %d", c, calls)
	}
	if c := post(work, "good"); c != http.StatusNoContent || calls != 1 {
		t.Fatalf("delivery: %d, calls %d", c, calls)
	}
	failNext = true
	if c := post(work, "good"); c != http.StatusInternalServerError {
		t.Fatalf("a failing handler must answer 500 so the queue retries, got %d", c)
	}
	if c := post(jobs.WorkPath(deliverable.KindDeliver, id), "good"); c != http.StatusNotFound {
		t.Fatalf("a kind without a handler: %d", c)
	}
	if c := post("/internal/work/crew/not-a-uuid", "good"); c != http.StatusBadRequest {
		t.Fatalf("bad id: %d", c)
	}
	if c := post(jobs.TickPath, "good"); c != http.StatusNoContent || ticks != 1 {
		t.Fatalf("tick: %d, ticks %d", c, ticks)
	}
	if c := post(jobs.TickPath, ""); c != http.StatusUnauthorized {
		t.Fatalf("unauthenticated tick: %d", c)
	}

	// Without the cloud settings the routes simply do not exist.
	local := httptest.NewServer((&api.Server{Store: e.store, Hub: hub.New(e.store, logger), Broker: events.NewBroker(),
		Scheduler: e.sched, Crew: e.crew, Logger: logger}).Handler())
	defer local.Close()
	resp, err := http.Post(local.URL+jobs.TickPath, "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		t.Fatal("the local deployment must not expose /internal/tick")
	}
}

// A run's tool server works from any instance: here the agent talks to instance B while instance A
// owns the run. B rebuilds the run from its database record, a terminal tool call served by B is
// recorded, and A (which is waiting) sees it and finishes the task.
func TestPerRunMCPAcrossInstances(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	b := crew.New(e.store, hub.New(e.store, logger), events.NewBroker(), logger, false)
	mux := http.NewServeMux()
	mux.Handle(crew.MCPPathPrefix, b.MCPHandler(0))
	instanceB := httptest.NewServer(mux)
	defer instanceB.Close()

	// A is configured for the cloud (queue set), which turns on its outcome watcher.
	e.crew.Queue = &fakeQueue{}
	e.crew.MCPBaseURL = instanceB.URL

	var rt store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "Fake agent", "type": "acp_agent", "config": map[string]any{
		"preset": "custom", "command": os.Args[0], "env": map[string]any{"CUEFLOW_FAKE_ACP": "1"}, "timeout_minutes": 1,
	}}, &rt); code != 201 {
		t.Fatalf("create runtime connector: %d", code)
	}
	var agent store.Agent
	if code := e.do("POST", "/agents", map[string]any{"name": "Remote writer", "slug": "remote-writer", "capabilities": []string{"post_deliverables"},
		"runtime_connector_id": rt.ID}, &agent); code != 201 {
		t.Fatalf("create agent: %d", code)
	}
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Write about ACP", "assignee_agent_id": agent.ID}, &task)

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.crew.RunPending(ctx, 3)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the run did not finish: instance A never learned of the terminal call served by B")
	}

	got, _ := e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskReview || !strings.Contains(got.Result, "Done via ACP") {
		t.Fatalf("task: %s %q", got.Status, got.Result)
	}
	posts, _ := e.ds.ForTask(ctx, task.ID)
	if len(posts) != 1 || posts[0].Status != deliverable.StatusPendingReview {
		t.Fatalf("the draft made through instance B is missing: %+v", posts)
	}
	var tokens int
	if err := e.pool.QueryRow(ctx, `SELECT COUNT(*) FROM run_tokens`).Scan(&tokens); err != nil || tokens != 0 {
		t.Fatalf("the run's credential must be gone once the run ends: %d %v", tokens, err)
	}
	resp, err := http.Post(instanceB.URL+crew.MCPPathPrefix+"whatever", "application/json", strings.NewReader("{}"))
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown run token should 404: %v %v", err, resp)
	}
	resp.Body.Close()
}

// Canceling a task on one instance stops the agent running it on another: the running instance's
// heartbeat finds the task is no longer in progress and cancels the run.
func TestCancelReachesOtherInstance(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	e.crew.Lease = 300 * time.Millisecond // heartbeat every 100 ms
	started := make(chan struct{})
	var once sync.Once
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		once.Do(func() { close(started) })
		time.Sleep(5 * time.Second) // a slow model call; the run is canceled meanwhile
		return calls("complete_task", map[string]any{"summary": "too late"})
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Slow", "assignee_agent_id": team["chief"].ID}, &task)
	go e.crew.RunPending(ctx, 1)
	<-started

	// Another instance cancels the task: it only touches the database.
	if ok, err := e.store.SetTaskStatusIf(ctx, task.ID, []store.TaskStatus{store.TaskInProgress}, store.TaskCanceled, nil); err != nil || !ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var open int
		_ = e.pool.QueryRow(ctx, `SELECT COUNT(*) FROM agent_runs WHERE task_id=$1 AND status='RUNNING'`, task.ID).Scan(&open)
		if open == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var open int
	_ = e.pool.QueryRow(ctx, `SELECT COUNT(*) FROM agent_runs WHERE task_id=$1 AND status='RUNNING'`, task.ID).Scan(&open)
	if open != 0 {
		t.Fatal("the run kept going after the task was canceled elsewhere")
	}
	if got, _ := e.store.GetTask(ctx, task.ID); got.Status != store.TaskCanceled {
		t.Fatalf("task should stay canceled, is %s", got.Status)
	}
}
