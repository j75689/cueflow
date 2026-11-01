package e2e

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

var errConnReset = errors.New("llm: read tcp: connection reset by peer")

// setFail makes the script model fail the calls f returns an error for (see scriptErr).
func setFail(t *testing.T, f func(req llm.Request) error) {
	scriptMu.Lock()
	scriptErr = f
	scriptMu.Unlock()
	t.Cleanup(func() {
		scriptMu.Lock()
		scriptErr = nil
		scriptMu.Unlock()
	})
}

func alwaysFail(llm.Request) error { return errConnReset }

// withNotifier points the org's alerts at the fake notifier and drops the alerts sent so far.
func withNotifier(t *testing.T, e *env) {
	t.Helper()
	org, _ := e.store.DefaultOrg(context.Background())
	var n store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "TG", "type": "fake_notifier", "config": map[string]any{}}, &n); code != 201 {
		t.Fatalf("create notifier: %d", code)
	}
	if code := e.do("PATCH", "/orgs/"+org.ID.String(), map[string]any{"notifier_id": n.ID}, nil); code != 200 {
		t.Fatalf("pick notifier: %d", code)
	}
	takeAlerts()
}

func subjects(ns []connector.Notification) string {
	var out []string
	for _, n := range ns {
		out = append(out, n.Subject)
	}
	return strings.Join(out, " | ")
}

func setAutoRecover(t *testing.T, e *env, a *store.Agent, n int) {
	t.Helper()
	if code := e.do("PATCH", "/agents/"+a.ID.String(), map[string]any{"auto_recover": n}, nil); code != 200 {
		t.Fatalf("auto_recover: %d", code)
	}
}

func outcomes(t *testing.T, e *env, task store.Task) string {
	t.Helper()
	var runs []store.AgentRun
	e.do("GET", "/tasks/"+task.ID.String()+"/runs", nil, &runs)
	var out []string
	for _, r := range runs {
		out = append(out, r.Outcome)
	}
	return strings.Join(out, ",")
}

func newTask(t *testing.T, e *env, title string, a *store.Agent) store.Task {
	t.Helper()
	var task store.Task
	if code := e.do("POST", "/tasks", map[string]any{"title": title, "assignee_agent_id": a.ID}, &task); code != 201 {
		t.Fatalf("create task: %d", code)
	}
	return task
}

func status(t *testing.T, e *env, task store.Task) store.TaskStatus {
	t.Helper()
	got, err := e.store.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got.Status
}

// Off by default: a failed run waits for the human with today's one alert.
func TestAutoRecoverOffByDefault(t *testing.T) {
	e, team := crewSetup(t)
	withNotifier(t, e)
	setFail(t, alwaysFail)
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		return calls("complete_task", map[string]any{"summary": "done"})
	})
	task := newTask(t, e, "Flaky", team["writer"])
	runAll(e)
	if got := outcomes(t, e, task); got != "error" || status(t, e, task) != store.TaskWaitingHuman {
		t.Fatalf("runs %s, status %s", got, status(t, e, task))
	}
	if got := subjects(takeAlerts()); got != "❌ Run failed: Flaky" {
		t.Fatalf("alerts: %s", got)
	}
	if thread := taskMessages(t, e, task.ID.String()); strings.Contains(thread, "Auto-recover") {
		t.Fatalf("thread:\n%s", thread)
	}
}

// With a limit of 2, a failure that keeps happening is retried twice, with one alert each, and the
// third gives up with one alert. Pressing Run after that starts the count again.
func TestAutoRecoverRetriesThenGivesUp(t *testing.T) {
	e, team := crewSetup(t)
	writer := team["writer"]
	withNotifier(t, e)
	setAutoRecover(t, e, writer, 2)
	var a store.Agent
	if e.do("GET", "/agents/"+writer.ID.String(), nil, &a); a.AutoRecover != 2 {
		t.Fatalf("auto_recover not saved: %d", a.AutoRecover)
	}
	setFail(t, alwaysFail)
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		return calls("complete_task", map[string]any{"summary": "done"})
	})
	task := newTask(t, e, "Flaky", writer)
	runAll(e)

	if got := outcomes(t, e, task); got != "error,retry,retry" || status(t, e, task) != store.TaskWaitingHuman {
		t.Fatalf("runs %s, status %s", got, status(t, e, task))
	}
	thread := taskMessages(t, e, task.ID.String())
	for _, want := range []string{"The agent run failed: llm: read tcp: connection reset by peer",
		"Auto-recover 1 of 2: a fresh run starts in 0s", "Auto-recover 2 of 2", "Auto-recover gave up after 2 attempts in a row"} {
		if !strings.Contains(thread, want) {
			t.Fatalf("thread misses %q:\n%s", want, thread)
		}
	}
	sent := takeAlerts()
	if got := subjects(sent); got != "❌ Run failed, auto-recovering: Flaky | ❌ Run failed, auto-recovering: Flaky | 🛑 Run failed, auto-recover gave up: Flaky" {
		t.Fatalf("alerts: %s", got)
	}
	if !strings.Contains(sent[1].Text, "Auto-recover 2 of 2: retrying in 0s.") || !strings.Contains(sent[2].Text, "gave up after 2 attempts") {
		t.Fatalf("alert texts: %+v", sent)
	}

	if code := e.do("POST", "/tasks/"+task.ID.String()+"/run", nil, nil); code != 200 {
		t.Fatalf("run: %d", code)
	}
	runAll(e)
	if got := outcomes(t, e, task); got != "error,retry,retry,error,retry,retry" {
		t.Fatalf("after Run: runs %s", got)
	}
	if n := strings.Count(taskMessages(t, e, task.ID.String()), "Auto-recover 1 of 2"); n != 2 {
		t.Fatalf("the count should start again after giving up: %d", n)
	}
}

// A run that fails once is retried, and the fresh run starts from what the failed one left in the thread.
func TestAutoRecoverSucceeds(t *testing.T) {
	e, team := crewSetup(t)
	writer := team["writer"]
	withNotifier(t, e)
	setAutoRecover(t, e, writer, 3)
	failed := false
	setFail(t, func(req llm.Request) error {
		if len(req.Messages) > 1 && !failed {
			failed = true
			return errConnReset
		}
		return nil
	})
	sawProgress := false
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		if strings.Contains(brief(req), "step one done") {
			sawProgress = true
			return calls("complete_task", map[string]any{"summary": "All done."})
		}
		return calls("add_comment", map[string]any{"content": "step one done"})
	})
	task := newTask(t, e, "Flaky", writer)
	runAll(e)
	if got := outcomes(t, e, task); got != "complete,retry" || status(t, e, task) != store.TaskReview || !sawProgress {
		t.Fatalf("runs %s, status %s, fresh run saw the progress: %v", got, status(t, e, task), sawProgress)
	}
	if got := subjects(takeAlerts()); got != "❌ Run failed, auto-recovering: Flaky | ✅ Ready for review: Flaky" {
		t.Fatalf("alerts: %s", got)
	}
}

// A run that used its turn budget is not a transient failure.
func TestAutoRecoverNotForPermanentFailures(t *testing.T) {
	e, team := crewSetup(t)
	writer := team["writer"]
	setAutoRecover(t, e, writer, 3)
	if code := e.do("PATCH", "/agents/"+writer.ID.String(), map[string]any{"max_turns": 1}, nil); code != 200 {
		t.Fatalf("max_turns: %d", code)
	}
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		return calls("add_comment", map[string]any{"content": "working"})
	})
	task := newTask(t, e, "Long", writer)
	runAll(e)
	if got := outcomes(t, e, task); got != "limit" || status(t, e, task) != store.TaskWaitingHuman {
		t.Fatalf("runs %s, status %s", got, status(t, e, task))
	}
}

// A recovered task waits out its backoff in TODO: nothing claims it, and the cloud tick sees nothing to
// do. A comment runs it at once and starts the count again; so does Run; Stop parks it.
func TestAutoRecoverWaitsForRetryAt(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	e.crew.RecoverBackoff = func(int) time.Duration { return time.Hour }
	setAutoRecover(t, e, team["writer"], 3)
	setFail(t, alwaysFail)
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		return calls("complete_task", map[string]any{"summary": "done"})
	})
	task := newTask(t, e, "Flaky", team["writer"])
	runAll(e)
	waiting := func() bool {
		var ok bool
		if err := e.pool.QueryRow(ctx, `SELECT retry_at > NOW() FROM tasks WHERE id=$1`, task.ID).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if got := outcomes(t, e, task); got != "retry" || status(t, e, task) != store.TaskTodo || !waiting() {
		t.Fatalf("runs %s, status %s, waiting %v", got, status(t, e, task), waiting())
	}
	if n, err := e.store.TodoTaskCount(ctx); err != nil || n != 0 {
		t.Fatalf("a task in its backoff counts as work: %d %v", n, err)
	}
	// Other work in the org is claimed meanwhile; the waiting task is not.
	other := newTask(t, e, "Other", team["editor"])
	runAll(e)
	if got := outcomes(t, e, task); got != "retry" || outcomes(t, e, other) != "error" {
		t.Fatalf("runs %s, other's %s", got, outcomes(t, e, other))
	}
	if !strings.Contains(taskMessages(t, e, task.ID.String()), "Auto-recover 1 of 3: a fresh run starts in 60m") {
		t.Fatal("no auto-recover line")
	}

	if code := e.do("POST", "/tasks/"+task.ID.String()+"/messages", map[string]any{"content": "try again"}, nil); code != 201 {
		t.Fatalf("comment: %d", code)
	}
	runAll(e)
	thread := taskMessages(t, e, task.ID.String())
	if got := outcomes(t, e, task); got != "retry,retry" || strings.Count(thread, "Auto-recover 1 of 3") != 2 {
		t.Fatalf("a comment should run it now and reset the count: runs %s\n%s", got, thread)
	}

	if code := e.do("POST", "/tasks/"+task.ID.String()+"/run", nil, nil); code != 200 {
		t.Fatalf("run: %d", code)
	}
	runAll(e)
	thread = taskMessages(t, e, task.ID.String())
	if got := outcomes(t, e, task); got != "retry,retry,retry" || strings.Count(thread, "Auto-recover 1 of 3") != 3 {
		t.Fatalf("Run should retry now and reset the count: runs %s\n%s", got, thread)
	}

	if code := e.do("POST", "/tasks/"+task.ID.String()+"/cancel", nil, nil); code != 200 {
		t.Fatalf("stop: %d", code)
	}
	var cleared bool
	if err := e.pool.QueryRow(ctx, `SELECT retry_at IS NULL FROM tasks WHERE id=$1`, task.ID).Scan(&cleared); err != nil || !cleared ||
		status(t, e, task) != store.TaskWaitingHuman {
		t.Fatalf("Stop: status %s, retry_at cleared %v %v", status(t, e, task), cleared, err)
	}
}

// Moving the card starts the count again (decision 6), as does Stop then Run; without either, the next
// failure in a row counts on.
func TestAutoRecoverCountResetsOnAHumanMove(t *testing.T) {
	e, team := crewSetup(t)
	e.crew.RecoverBackoff = func(int) time.Duration { return time.Hour }
	setAutoRecover(t, e, team["writer"], 3)
	setFail(t, alwaysFail)
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		return calls("complete_task", map[string]any{"summary": "done"})
	})
	task := newTask(t, e, "Flaky", team["writer"])
	id := task.ID.String()
	runAll(e)
	if got := outcomes(t, e, task); got != "retry" || status(t, e, task) != store.TaskTodo {
		t.Fatalf("runs %s, status %s", got, status(t, e, task))
	}

	// During the backoff, Backlog then To do: the next failure is 1 of 3 again, not 2 of 3.
	for _, st := range []string{"BACKLOG", "TODO"} {
		if code := e.do("PATCH", "/tasks/"+id, map[string]any{"status": st}, nil); code != 200 {
			t.Fatalf("move to %s: %d", st, code)
		}
	}
	runAll(e)
	thread := taskMessages(t, e, id)
	if got := outcomes(t, e, task); got != "retry,retry" || strings.Count(thread, "Auto-recover 1 of 3") != 2 || strings.Contains(thread, "2 of 3") {
		t.Fatalf("moving the card should reset the count: runs %s\n%s", got, thread)
	}

	// Stop, then Run: 1 of 3 again.
	for _, op := range []string{"cancel", "run"} {
		if code := e.do("POST", "/tasks/"+id+"/"+op, nil, nil); code != 200 {
			t.Fatalf("%s: %d", op, code)
		}
	}
	runAll(e)
	thread = taskMessages(t, e, id)
	if got := outcomes(t, e, task); got != "retry,retry,retry" || strings.Count(thread, "Auto-recover 1 of 3") != 3 || strings.Contains(thread, "2 of 3") {
		t.Fatalf("Stop then Run should reset the count: runs %s\n%s", got, thread)
	}

	// No human move: the next failure in a row counts on.
	if _, err := e.pool.Exec(context.Background(), `UPDATE tasks SET retry_at=NOW() WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	runAll(e)
	if !strings.Contains(taskMessages(t, e, id), "Auto-recover 2 of 3") {
		t.Fatalf("the count should go on: runs %s", outcomes(t, e, task))
	}
}

// Auto hand-off and auto-recover together: when every second fresh run fails, the retries in between
// don't start the hand-off count again, so it still stops at the hand-off cap (3 in a row) instead of
// alternating retry, handoff_auto for as long as the loop runs.
func TestAutoRecoverInterleavedWithAutoHandOff(t *testing.T) {
	e, team := crewSetup(t)
	writer := team["writer"]
	if code := e.do("PATCH", "/agents/"+writer.ID.String(), map[string]any{"auto_handoff": true}, nil); code != 200 {
		t.Fatalf("auto_handoff: %d", code)
	}
	setAutoRecover(t, e, writer, 1)
	setPrompt(t, func(req llm.Request) int64 {
		if len(req.Messages) == 1 {
			return 100
		}
		return 900
	})
	starts := 0
	setFail(t, func(req llm.Request) error {
		if len(req.Messages) == 1 {
			starts++
			if starts%2 == 0 {
				return errConnReset
			}
		}
		return nil
	})
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		if asksForHandOff(req) {
			return calls("write_file", map[string]any{"name": "handoff.md", "content": "progress so far"})
		}
		return calls("add_comment", map[string]any{"content": "working"})
	})
	task := newTask(t, e, "Long and flaky", writer)
	for range 60 {
		e.crew.RunPending(context.Background(), 10)
	}
	thread := taskMessages(t, e, task.ID.String())
	// Newest first.
	if got := outcomes(t, e, task); got != "handoff,retry,handoff_auto,retry,handoff_auto,retry,handoff_auto" ||
		status(t, e, task) != store.TaskWaitingHuman {
		t.Fatalf("runs %s, status %s\n%s", got, status(t, e, task), thread)
	}
	if !strings.Contains(thread, "automatic hand-off 3 of 3") || !strings.Contains(thread, "This is the 4th hand-off in a row") ||
		strings.Count(thread, "Auto-recover 1 of 1") != 3 {
		t.Fatalf("thread:\n%s", thread)
	}
}

// The same for a built-in run that continues past its delivery: the retries in between don't start
// the continue count again, so it stops at 5 in a row instead of alternating retry, continued.
func TestAutoRecoverInterleavedWithDeliveryContinue(t *testing.T) {
	e, team := crewSetup(t)
	e.crew.Queue = &fakeQueue{}
	e.crew.DeliveryLimit = 1500 * time.Millisecond // no new turn after 1.25 s
	writer := team["writer"]
	setAutoRecover(t, e, writer, 1)
	starts := 0
	setFail(t, func(req llm.Request) error {
		if len(req.Messages) == 1 {
			starts++
			if starts%2 == 0 {
				return errConnReset
			}
		}
		return nil
	})
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		time.Sleep(1300 * time.Millisecond) // every turn uses the delivery's time
		return calls("add_comment", map[string]any{"content": "working"})
	})
	task := newTask(t, e, "Long and flaky", writer)
	for range 30 {
		if status(t, e, task) != store.TaskTodo {
			break
		}
		if err := e.crew.HandleCrew(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	thread := taskMessages(t, e, task.ID.String())
	// Newest first.
	if got := outcomes(t, e, task); got != "continue,retry,continued,retry,continued,retry,continued,retry,continued,retry,continued" ||
		status(t, e, task) != store.TaskWaitingHuman {
		t.Fatalf("runs %s, status %s\n%s", got, status(t, e, task), thread)
	}
	if !strings.Contains(thread, "(5 of 5 in a row)") || !strings.Contains(thread, "for the 6th time in a row") {
		t.Fatalf("thread:\n%s", thread)
	}
}

// A run interrupted in this process (the server shutting down) is recovered too.
func TestAutoRecoverInterrupted(t *testing.T) {
	e, team := crewSetup(t)
	withNotifier(t, e)
	setAutoRecover(t, e, team["writer"], 1)
	ctx, cancel := context.WithCancel(context.Background())
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		if ctx.Err() == nil {
			cancel()
			return calls("add_comment", map[string]any{"content": "working"})
		}
		return calls("complete_task", map[string]any{"summary": "done"})
	})
	task := newTask(t, e, "Shutdown", team["writer"])
	e.crew.RunPending(ctx, 1)
	if got := outcomes(t, e, task); got != "retry" || status(t, e, task) != store.TaskTodo {
		t.Fatalf("runs %s, status %s", got, status(t, e, task))
	}
	runAll(e)
	if got := outcomes(t, e, task); got != "complete,retry" {
		t.Fatalf("runs %s", got)
	}
	if got := subjects(takeAlerts()); got != "⚠️ Interrupted, auto-recovering: Shutdown | ✅ Ready for review: Shutdown" {
		t.Fatalf("alerts: %s", got)
	}
}

// A run whose lease lapsed (its server crashed) is recovered by the sweep too, with one merged alert,
// and the next lapse past the limit gives up.
func TestAutoRecoverLeaseExpired(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	writer := team["writer"]
	withNotifier(t, e)
	setAutoRecover(t, e, writer, 1)
	task := newTask(t, e, "Crashy", writer)
	crash := func() {
		t.Helper()
		runID := uuid.New()
		if _, err := e.store.StartAgentRun(ctx, runID, task.ID, writer.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `UPDATE tasks SET status='IN_PROGRESS', run_id=$2, lease_until=NOW() - INTERVAL '1 minute'
			WHERE id=$1`, task.ID, runID); err != nil {
			t.Fatal(err)
		}
		if err := e.crew.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}

	crash()
	if got := outcomes(t, e, task); got != "retry" || status(t, e, task) != store.TaskTodo {
		t.Fatalf("runs %s, status %s", got, status(t, e, task))
	}
	if got := subjects(takeAlerts()); got != "⚠️ Interrupted, auto-recovering: Crashy" {
		t.Fatalf("alerts: %s", got)
	}

	crash()
	if status(t, e, task) != store.TaskWaitingHuman || !strings.Contains(taskMessages(t, e, task.ID.String()), "Auto-recover gave up after 1 attempts") {
		t.Fatalf("status %s\n%s", status(t, e, task), taskMessages(t, e, task.ID.String()))
	}
	if got := subjects(takeAlerts()); got != "🛑 Run failed, auto-recover gave up: Crashy" {
		t.Fatalf("alerts: %s", got)
	}
}

// No runner picking up the work is retried; each retry's job is expired, so none is left queued.
func TestAutoRecoverRunnerNotPickedUp(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.crew.MCPBaseURL = e.api.URL
	var pool store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "Offline", "type": "runner_pool", "config": map[string]any{
		"preset": "custom", "labels": []string{"nobody"}, "queue_timeout_minutes": 0,
	}}, &pool); code != 201 {
		t.Fatalf("create runner pool: %d", code)
	}
	var agent store.Agent
	if code := e.do("POST", "/agents", map[string]any{"name": "Remote", "slug": "remote", "capabilities": []string{},
		"runtime_connector_id": pool.ID, "auto_recover": 1}, &agent); code != 201 || agent.AutoRecover != 1 {
		t.Fatalf("create agent: %d %+v", code, agent)
	}
	task := newTask(t, e, "Remote work", &agent)
	runAll(e)
	if got := outcomes(t, e, task); got != "error,retry" || status(t, e, task) != store.TaskWaitingHuman {
		t.Fatalf("runs %s, status %s", got, status(t, e, task))
	}
	var jobs, live int
	if err := e.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status IN ('queued','running','canceling'))
		FROM runner_jobs WHERE task_id=$1`, task.ID).Scan(&jobs, &live); err != nil || jobs != 2 || live != 0 {
		t.Fatalf("runner jobs: %d, live %d, %v", jobs, live, err)
	}
}

// The limit is 0..10, kept by the store and the database, and travels with a team template.
func TestAutoRecoverSetting(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	writer := team["writer"]
	var a store.Agent
	for _, c := range []struct{ in, want int }{{99, 10}, {-1, 0}, {3, 3}} {
		if code := e.do("PATCH", "/agents/"+writer.ID.String(), map[string]any{"auto_recover": c.in}, &a); code != 200 || a.AutoRecover != c.want {
			t.Fatalf("auto_recover %d: %d, saved %d", c.in, code, a.AutoRecover)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE agents SET auto_recover=11 WHERE id=$1`, writer.ID); err == nil {
		t.Fatal("the database took auto_recover 11")
	}
	var tmpl struct {
		Agents []struct {
			Slug        string `json:"slug"`
			AutoRecover int    `json:"auto_recover"`
		} `json:"agents"`
	}
	e.do("GET", "/agents/template", nil, &tmpl)
	for _, ta := range tmpl.Agents {
		if ta.Slug == "writer" && ta.AutoRecover != 3 {
			t.Fatalf("template: %+v", ta)
		}
	}
}
