package e2e

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/runner"
	"github.com/j75689/cueflow/internal/store"
)

// A task whose agent uses a runner_pool runtime is worked by a registered runner: the server queues
// a job, the runner claims it, runs the (fake) ACP agent locally, and the agent's tool calls reach
// CueFlow's per-run MCP server exactly as for a host-local agent.
func TestRunnerExecutesTask(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.crew.MCPBaseURL = e.api.URL

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

	// Register through the real API with an admin-issued token.
	var reg struct {
		Token string `json:"token"`
	}
	if code := e.do("POST", "/runners/registrations", map[string]any{"labels": []string{"fake"}}, &reg); code != 201 {
		t.Fatalf("registration: %d", code)
	}
	cfgPath := t.TempDir() + "/runner.json"
	if err := runner.Main([]string{"register", "--url", e.api.URL, "--token", reg.Token, "--name", "test-box", "--config", cfgPath}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := runner.Main([]string{"register", "--url", e.api.URL, "--token", reg.Token, "--name", "again", "--config", cfgPath + "2"}, "test"); err == nil {
		t.Fatal("a registration token must work only once")
	}
	cfg, err := runner.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Commands = map[string]string{"custom": os.Args[0]}
	cfg.Env = map[string]string{"CUEFLOW_FAKE_ACP": "1"}
	every := 1 // poll every second (the default 20 s would only make this test slow): the short-poll path
	cfg.PollInterval = &every

	var runners []store.Runner
	e.do("GET", "/runners", nil, &runners)
	if len(runners) != 1 || runners[0].Name != "test-box" || len(runners[0].Labels) != 1 || runners[0].Labels[0] != "fake" {
		t.Fatalf("runners: %+v", runners)
	}

	loopDone := make(chan error, 1)
	go func() {
		loopDone <- runner.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Loop(ctx)
	}()

	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Write about ACP", "assignee_agent_id": agent.ID}, &task)
	e.crew.RunPending(ctx, 3)

	got, _ := e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskReview || !strings.Contains(got.Result, "bash_refused=true") {
		t.Fatalf("task: %s %q", got.Status, got.Result)
	}
	posts, _ := e.ds.ForTask(ctx, task.ID)
	if len(posts) != 1 || posts[0].Status != deliverable.StatusPendingReview {
		t.Fatalf("draft not created by the runner's agent: %+v", posts)
	}
	var msgs []store.TaskMessage
	e.do("GET", "/tasks/"+task.ID.String()+"/messages", nil, &msgs)
	var thread []string
	for _, m := range msgs {
		thread = append(thread, m.Kind+":"+m.Content)
	}
	joined := strings.ReplaceAll(strings.Join(thread, "\n"), `\"`, `"`)
	for _, want := range []string{`Picked up by runner "test-box"`, "message:Reading the task.", `Refused "Bash: rm -rf /"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("thread missing %q:\n%s", want, joined)
		}
	}
	var jobs []store.RunnerJob
	e.do("GET", "/tasks/"+task.ID.String()+"/runner-jobs", nil, &jobs)
	if len(jobs) != 1 || !store.JobFinished(jobs[0].Status) || jobs[0].RunnerName != "test-box" {
		t.Fatalf("jobs: %+v", jobs)
	}
	if len(e.mock.Posts()) != 0 {
		t.Fatal("a runner's agent must never publish")
	}

	// Revoking the runner stops it: its token no longer authenticates.
	e.do("DELETE", "/runners/"+runners[0].ID.String(), nil, nil)
	select {
	case err := <-loopDone:
		if err == nil {
			t.Fatal("expected the runner to stop with an error after revocation")
		}
	case <-time.After(40 * time.Second):
		t.Fatal("revoked runner kept running")
	}
}

// A runner takes only jobs whose labels it carries and whose org it serves.
func TestRunnerJobMatching(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	org, _ := e.store.DefaultOrg(ctx)
	var agent store.Agent
	e.do("POST", "/agents", map[string]any{"name": "A", "slug": "a", "capabilities": []string{"post_deliverables"}}, &agent)
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "T", "assignee_agent_id": agent.ID}, &task)
	if _, err := e.store.CreateRunnerJob(ctx, store.NewRunnerJob{OrgID: org.ID, TaskID: task.ID, Labels: []string{"gpu", "linux"}, Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	reg := func(labels ...string) *store.Runner {
		tok, _, err := e.store.CreateRegistration(ctx, labels, nil, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		r, _, err := e.store.RegisterRunner(ctx, tok, "r", "t", "p", nil, 1)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if j, _ := e.store.ClaimRunnerJob(ctx, reg("linux")); j != nil {
		t.Fatal("a runner missing a required label must not get the job")
	}
	full := reg("gpu", "linux", "extra")
	j, err := e.store.ClaimRunnerJob(ctx, full)
	if err != nil || j == nil || j.Status != store.JobRunning {
		t.Fatalf("matching runner should claim: %v %+v", err, j)
	}
	if j2, _ := e.store.ClaimRunnerJob(ctx, full); j2 != nil {
		t.Fatal("a claimed job must not be handed out twice")
	}
}

// Behind an access proxy (Cloudflare Access) that turns away requests without its service token, a runner
// with the token in its headers registers, claims the job, and its agent finishes the task through the
// runner's loopback proxy, without being given the token.
func TestRunnerBehindAccessProxy(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const secret = "access-s3cret"
	api, _ := url.Parse(e.api.URL)
	rp := httputil.NewSingleHostReverseProxy(api)
	rp.FlushInterval = -1
	var turnedAway atomic.Int32
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("CF-Access-Client-Id") != "id.access" || r.Header.Get("CF-Access-Client-Secret") != secret {
			turnedAway.Add(1)
			http.Redirect(w, r, "https://team.cloudflareaccess.com/cdn-cgi/access/login", http.StatusFound)
			return
		}
		rp.ServeHTTP(w, r)
	}))
	defer front.Close()

	var pool store.Connector
	e.do("POST", "/connectors", map[string]any{"name": "Behind Access", "type": "runner_pool", "config": map[string]any{
		"preset": "custom", "labels": []string{"fake"}, "timeout_minutes": 1}}, &pool)
	var agent store.Agent
	e.do("POST", "/agents", map[string]any{"name": "Remote writer", "slug": "remote-writer",
		"capabilities": []string{"post_deliverables"}, "runtime_connector_id": pool.ID}, &agent)
	var reg struct {
		Token string `json:"token"`
	}
	e.do("POST", "/runners/registrations", map[string]any{"labels": []string{"fake"}}, &reg)

	cfgPath := t.TempDir() + "/runner.json"
	t.Setenv("CUEFLOW_RUNNER_HEADERS", "CF-Access-Client-Secret: "+secret)
	if err := runner.Main([]string{"register", "--url", front.URL, "--token", reg.Token, "--name", "behind-access",
		"--header", "CF-Access-Client-Id: id.access", "--config", cfgPath}, "test"); err != nil {
		t.Fatal(err)
	}
	cfg, err := runner.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Commands = map[string]string{"custom": os.Args[0]}
	cfg.Env = map[string]string{"CUEFLOW_FAKE_ACP": "1", "FAKE_ACP_FORBID": secret}
	every := 1
	cfg.PollInterval = &every
	go func() { _ = runner.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Loop(ctx) }()

	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Write about ACP", "assignee_agent_id": agent.ID}, &task)
	e.crew.RunPending(ctx, 3)

	got, _ := e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskReview || !strings.Contains(got.Result, "bash_refused=true") {
		t.Fatalf("task: %s %q", got.Status, got.Result)
	}
	if posts, _ := e.ds.ForTask(ctx, task.ID); len(posts) != 1 {
		t.Fatalf("the agent's tool calls did not reach CueFlow: %+v", posts)
	}
	if n := turnedAway.Load(); n != 0 {
		t.Fatalf("%d requests went without the headers", n)
	}
}
