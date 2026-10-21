package e2e

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/j75689/cueflow/internal/jobs"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/runner"
	"github.com/j75689/cueflow/internal/store"
)

func threadText(t *testing.T, e *env, taskID string) string {
	t.Helper()
	var msgs []store.TaskMessage
	e.do("GET", "/tasks/"+taskID+"/messages", nil, &msgs)
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Kind + ":" + m.Content + "\n")
	}
	return b.String()
}

// A human stop reads "Stopped."; a run whose context ended any other way (the server stopping, the
// delivery's request cut off) is interrupted and says so.
func TestStoppedOnlyWhenAHumanStopsIt(t *testing.T) {
	e, team := crewSetup(t)
	e.crew.Queue = &fakeQueue{}

	for _, tc := range []struct{ name, want, status string }{
		{"human", "status:Stopped.", "CANCELED"},
		{"cut off", "The agent run was interrupted before it finished", "FAILED"},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		var task store.Task
		e.do("POST", "/tasks", map[string]any{"title": "Long " + tc.name, "assignee_agent_id": team["chief"].ID}, &task)
		setScript(t, func(t *testing.T, req llm.Request) llm.Message {
			if tc.name == "human" {
				e.crew.Cancel(task.ID)
			} else {
				cancel() // the delivery's request ends under the run
			}
			return calls("add_comment", map[string]any{"content": "working"})
		})
		_ = e.crew.HandleCrew(ctx)
		cancel()

		got, _ := e.store.GetTask(context.Background(), task.ID)
		thread := threadText(t, e, task.ID.String())
		runs, _ := e.store.ListAgentRuns(context.Background(), task.ID)
		if got.Status != store.TaskWaitingHuman || !strings.Contains(thread, tc.want) || len(runs) != 1 || runs[0].Status != tc.status {
			t.Fatalf("%s: %s, runs %+v, thread:\n%s", tc.name, got.Status, runs, thread)
		}
		if tc.name == "cut off" && strings.Contains(thread, "Stopped.") {
			t.Fatalf("a cut-off run must not read as stopped:\n%s", thread)
		}
	}
}

// A built-in run that uses its delivery's time stops between turns and continues in a fresh run
// from the next delivery.
func TestBuiltinRunContinuesInTheNextDelivery(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	q := &fakeQueue{}
	e.crew.Queue = q
	e.crew.DeliveryLimit = 3 * time.Second // no new turn after 2.5 s

	calls_ := 0
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		calls_++
		if calls_ == 1 {
			time.Sleep(2600 * time.Millisecond) // a long first turn
			return calls("add_comment", map[string]any{"content": "halfway"})
		}
		return calls("complete_task", map[string]any{"summary": "Finished in the second run."})
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Long job", "assignee_agent_id": team["chief"].ID}, &task)
	if err := e.crew.HandleCrew(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.store.GetTask(ctx, task.ID)
	thread := threadText(t, e, task.ID.String())
	if got.Status != store.TaskTodo || calls_ != 1 || q.count(jobs.KindCrew) == 0 ||
		!strings.Contains(thread, "continues in a fresh run (1 of 5 in a row)") {
		t.Fatalf("after the first delivery: %s, %d calls, %d deliveries queued:\n%s", got.Status, calls_, q.count(jobs.KindCrew), thread)
	}

	e.crew.DeliveryLimit = 30 * time.Minute
	if err := e.crew.HandleCrew(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = e.store.GetTask(ctx, task.ID)
	runs, _ := e.store.ListAgentRuns(ctx, task.ID)
	if got.Status != store.TaskReview || len(runs) != 2 || runs[1].Outcome != "continued" || runs[1].Status != "SUCCEEDED" {
		t.Fatalf("after the second delivery: %s %+v", got.Status, runs)
	}
}

// A runner's agent that outlives a delivery keeps running: the delivery hands the run over and the
// next one keeps watching the same job until it finishes.
func TestRunnerRunIsHandedOverToTheNextDelivery(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.crew.MCPBaseURL = e.api.URL
	q := &fakeQueue{}
	e.crew.Queue = q

	var pool store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "Laptop", "type": "runner_pool", "config": map[string]any{
		"preset": "custom", "labels": []string{"fake"}, "timeout_minutes": 1,
	}}, &pool); code != 201 {
		t.Fatalf("create runner pool: %d", code)
	}
	var agent store.Agent
	if code := e.do("POST", "/agents", map[string]any{"name": "Remote writer", "slug": "remote-writer",
		"capabilities": []string{"post_deliverables"}, "runtime_connector_id": pool.ID}, &agent); code != 201 {
		t.Fatalf("create agent: %d", code)
	}
	var reg struct {
		Token string `json:"token"`
	}
	e.do("POST", "/runners/registrations", map[string]any{"labels": []string{"fake"}}, &reg)
	cfgPath := t.TempDir() + "/runner.json"
	if err := runner.Main([]string{"register", "--url", e.api.URL, "--token", reg.Token, "--name", "box", "--config", cfgPath}, "test"); err != nil {
		t.Fatal(err)
	}
	cfg, err := runner.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Commands = map[string]string{"custom": os.Args[0]}
	cfg.Env = map[string]string{"CUEFLOW_FAKE_ACP": "1", "CUEFLOW_FAKE_ACP_DELAY": "5s"}
	every := 1
	cfg.PollInterval = &every
	go func() { _ = runner.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Loop(ctx) }()

	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Write about ACP", "assignee_agent_id": agent.ID}, &task)

	e.crew.DeliveryLimit = 3 * time.Second // hands over at 2.8 s, while the agent is still working
	if err := e.crew.HandleCrew(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.store.GetTask(ctx, task.ID)
	runs, _ := e.store.ListAgentRuns(ctx, task.ID)
	var jobs_ []store.RunnerJob
	e.do("GET", "/tasks/"+task.ID.String()+"/runner-jobs", nil, &jobs_)
	if got.Status != store.TaskInProgress || len(runs) != 1 || runs[0].Status != "RUNNING" || runs[0].Outcome != "handover" ||
		len(jobs_) != 1 || jobs_[0].Status != store.JobRunning || q.count(jobs.KindCrew) == 0 {
		t.Fatalf("after the first delivery: %s, runs %+v, jobs %+v, %d deliveries queued", got.Status, runs, jobs_, q.count(jobs.KindCrew))
	}

	e.crew.DeliveryLimit = 30 * time.Minute
	if err := e.crew.HandleCrew(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = e.store.GetTask(ctx, task.ID)
	runs, _ = e.store.ListAgentRuns(ctx, task.ID)
	e.do("GET", "/tasks/"+task.ID.String()+"/runner-jobs", nil, &jobs_)
	thread := threadText(t, e, task.ID.String())
	if got.Status != store.TaskReview || !strings.Contains(got.Result, "bash_refused=true") || len(runs) != 1 ||
		runs[0].Status != "SUCCEEDED" || len(jobs_) != 1 || strings.Contains(thread, "Stopped.") {
		t.Fatalf("after the second delivery: %s %q, runs %+v, jobs %+v:\n%s", got.Status, got.Result, runs, jobs_, thread)
	}
}
