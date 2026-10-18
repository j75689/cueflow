// Package e2e exercises the full CueFlow chain against a real PostgreSQL:
// RSS ingest -> agent drafts -> PENDING_REVIEW -> human approval via the API -> dispatch worker ->
// an MCP publisher (in-process mock) -> PUBLISHED, plus failure/retry and crash recovery.
//
// Set CUEFLOW_TEST_DATABASE_URL to a disposable database to run it (its public schema is reset).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/j75689/cueflow/internal/api"
	"github.com/j75689/cueflow/internal/connector"
	_ "github.com/j75689/cueflow/internal/connectors/all"
	"github.com/j75689/cueflow/internal/connectors/mcppublisher"
	"github.com/j75689/cueflow/internal/content"
	"github.com/j75689/cueflow/internal/content/dispatch"
	"github.com/j75689/cueflow/internal/content/pipeline"
	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/db"
	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/publishermock"
	"github.com/j75689/cueflow/internal/scheduler"
	"github.com/j75689/cueflow/internal/secret"
	"github.com/j75689/cueflow/internal/store"
)

// fakeLLM writes one draft per requested platform, citing S1, without any network.
type fakeLLM struct{}

func (fakeLLM) Model() string { return "fake" }
func (fakeLLM) Test(context.Context) (*connector.TestResult, error) {
	return &connector.TestResult{OK: true, Message: "fake"}, nil
}
func (fakeLLM) Chat(_ context.Context, req llm.Request) (*llm.Response, error) {
	prompt := req.Messages[0].Content
	var drafts []map[string]any
	for _, p := range []string{"threads", "instagram"} {
		if strings.Contains(prompt, fmt.Sprintf("platform id %q", p)) {
			drafts = append(drafts, map[string]any{
				"platform": p, "title": "Draft for " + p,
				"content":     "Agents are moving from chat boxes to ops consoles. " + p,
				"hashtags":    []string{"ai"},
				"source_refs": []string{"S1"},
			})
		}
	}
	args, _ := json.Marshal(map[string]any{"drafts": drafts})
	return &llm.Response{StopReason: "tool_use", Message: llm.Message{Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "1", Name: "submit_drafts", Arguments: args}}}}, nil
}

func init() {
	connector.Register(connector.Spec{Type: "fake_llm", Kind: connector.KindLLM, Name: "Fake LLM"},
		func(connector.Config, connector.Env) (connector.Instance, error) { return fakeLLM{}, nil })
}

const feed = `<?xml version="1.0"?><rss version="2.0"><channel><title>Test Feed</title>
<item><guid>a1</guid><title>Agent ops consoles are here</title><link>https://example.com/a1</link>
<description>&lt;p&gt;A long article about agents.&lt;/p&gt;</description><pubDate>%s</pubDate></item>
<item><guid>a2</guid><title>MCP adoption grows</title><link>https://example.com/a2</link>
<description>Second story.</description><pubDate>%s</pubDate></item>
</channel></rss>`

type env struct {
	t         *testing.T
	pool      *pgxpool.Pool
	store     *store.Store
	cs        *content.Store
	ds        *deliverable.Store
	deliverer *deliverable.Worker
	runner    *pipeline.Runner
	worker    *dispatch.Worker
	crew      *crew.Crew
	sched     *scheduler.Scheduler
	api       *httptest.Server
	server    *api.Server
	mock      *publishermock.Server
}

func setup(t *testing.T) *env {
	url := os.Getenv("CUEFLOW_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CUEFLOW_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	box, _ := secret.New(key)
	st := store.New(pool, box)
	cs := content.NewStore(st)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	broker := events.NewBroker()
	h := hub.New(st, logger)
	sched := scheduler.New(st, logger)
	runner := pipeline.NewRunner(ctx, cs, h, broker, logger, "http://localhost:3000")
	worker := dispatch.NewWorker(cs, h, broker, logger, time.Hour, time.Minute, 30*time.Second)
	module := &content.Module{Store: cs, Hub: h, Broker: broker, Scheduler: sched, Runner: runner, Dispatcher: worker,
		Logger: logger, PublicURL: "http://localhost:3000", MediaBaseURL: "http://localhost:3000"}
	module.Attach("")
	ds := deliverable.NewStore(st)
	deliverer := deliverable.NewWorker(ds, h, broker, logger, time.Hour, time.Minute, 30*time.Second)
	dmodule := &deliverable.Module{Store: ds, Hub: h, Broker: broker, Dispatcher: deliverer, Logger: logger}
	dmodule.Attach()
	team := crew.New(st, h, broker, logger, false)
	team.ValidateSchedule = scheduler.ValidateSchedule

	mock := publishermock.New("pub-token")
	mockSrv := httptest.NewServer(mock.Handler())
	t.Cleanup(mockSrv.Close)

	server := &api.Server{Store: st, Hub: h, Broker: broker, Scheduler: sched, Crew: team, Logger: logger,
		MCPToken: testMCPToken, Modules: []api.Module{module, dmodule}}
	apiSrv := httptest.NewServer(server.Handler())
	t.Cleanup(apiSrv.Close)

	e := &env{t: t, pool: pool, store: st, cs: cs, ds: ds, deliverer: deliverer, runner: runner, worker: worker, crew: team, sched: sched, api: apiSrv, server: server, mock: mock}
	t.Setenv("PUBLISHER_URL", mockSrv.URL)
	return e
}

func (e *env) do(method, path string, body any, out any) int {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, e.api.URL+"/api/v1"+path, r)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 500 {
		e.t.Logf("%s %s -> %d: %s", method, path, resp.StatusCode, data)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			e.t.Fatalf("%s %s: decode %q: %v", method, path, data, err)
		}
	}
	return resp.StatusCode
}

func TestFullChain(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	now := time.Now().UTC().Format(time.RFC1123Z)
	feedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprintf(w, feed, now, now)
	}))
	defer feedSrv.Close()

	// --- connectors via the API ---
	var rss, model, publisher store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "Feed", "type": "rss", "config": map[string]any{"url": feedSrv.URL}}, &rss); code != 201 {
		t.Fatalf("create rss: %d", code)
	}
	e.do("POST", "/connectors", map[string]any{"name": "LLM", "type": "fake_llm", "config": map[string]any{}}, &model)
	if code := e.do("POST", "/connectors", map[string]any{"name": "Publisher", "type": "mcp_publisher", "config": map[string]any{
		"endpoint_url": os.Getenv("PUBLISHER_URL") + "/mcp", "auth_token": "pub-token"}}, &publisher); code != 201 {
		t.Fatalf("create publisher: %d", code)
	}
	if len(publisher.SecretsSet) != 1 || publisher.Config["auth_token"] != nil {
		t.Fatalf("secret leaked into config or not stored: %+v", publisher)
	}
	var raw []byte
	_ = e.pool.QueryRow(ctx, "SELECT secrets FROM connectors WHERE id=$1", publisher.ID).Scan(&raw)
	if len(raw) == 0 || bytes.Contains(raw, []byte("pub-token")) {
		t.Fatal("bearer token must be stored encrypted")
	}
	var test connector.TestResult
	e.do("POST", "/connectors/"+publisher.ID.String()+"/test", nil, &test)
	if !test.OK {
		t.Fatalf("publisher test: %+v", test)
	}
	// Updating with an empty secret keeps the stored one.
	e.do("PATCH", "/connectors/"+publisher.ID.String(), map[string]any{"config": map[string]any{"auth_token": "", "list_tool": "list_accounts"}}, &publisher)
	if len(publisher.SecretsSet) != 1 {
		t.Fatalf("secret dropped on update: %+v", publisher)
	}

	// --- pipeline ---
	var p content.Pipeline
	code := e.do("POST", "/pipelines", map[string]any{
		"name": "Daily", "cron_expr": "0 8 * * *", "timezone": "Asia/Taipei", "topic": "AI agents",
		"platforms": []string{"threads", "instagram"}, "llm_connector_id": model.ID,
		"source_connector_ids": []uuid.UUID{rss.ID}, "publisher_connector_id": publisher.ID, "search_connector_id": "",
	}, &p)
	if code != 201 {
		t.Fatalf("create pipeline: %d", code)
	}
	if code := e.do("POST", "/pipelines", map[string]any{"name": "Bad", "cron_expr": "every day", "platforms": []string{"threads"}}, nil); code != 400 {
		t.Fatalf("invalid cron accepted: %d", code)
	}

	var started map[string]string
	if code := e.do("POST", "/pipelines/"+p.ID.String()+"/run", nil, &started); code != 202 {
		t.Fatalf("run: %d", code)
	}
	e.runner.Wait()
	var run content.Run
	e.do("GET", "/runs/"+started["run_id"], nil, &run)
	if run.Status != "SUCCEEDED" || run.Stats["posts_created"].(float64) != 2 || run.Stats["items_fetched"].(float64) != 2 {
		t.Fatalf("run: %+v", run)
	}

	// A second run finds nothing new (delta fetch + consumed items) and creates nothing.
	e.do("POST", "/pipelines/"+p.ID.String()+"/run", nil, &started)
	e.runner.Wait()
	e.do("GET", "/runs/"+started["run_id"], nil, &run)
	if run.Status != "SUCCEEDED" || run.Stats["posts_created"].(float64) != 0 || run.Stats["items_fetched"].(float64) != 0 {
		t.Fatalf("second run should be a no-op: %+v", run)
	}

	var list struct {
		Items []content.Post `json:"items"`
		Total int            `json:"total"`
	}
	e.do("GET", "/posts?status=PENDING_REVIEW", nil, &list)
	if list.Total != 2 {
		t.Fatalf("pending posts: %d", list.Total)
	}
	var threads, insta content.Post
	for _, post := range list.Items {
		switch post.Platforms[0] {
		case "threads":
			threads = post
		case "instagram":
			insta = post
		}
	}
	if !strings.Contains(string(threads.SourceContext), "https://example.com/a1") {
		t.Errorf("source context missing cited item: %s", threads.SourceContext)
	}
	if !strings.Contains(strings.Join(insta.Warnings, ";"), "requires at least one image") {
		t.Errorf("instagram media warning missing: %v", insta.Warnings)
	}

	// --- review rules ---
	var apiErr struct {
		Error struct{ Code string } `json:"error"`
	}
	if code := e.do("POST", "/posts/"+threads.ID.String()+"/retry", nil, &apiErr); code != 409 || apiErr.Error.Code != "invalid_transition" {
		t.Fatalf("retry from PENDING_REVIEW: %d %+v", code, apiErr)
	}
	if code := e.do("PATCH", "/posts/"+threads.ID.String(), map[string]any{"version": threads.Version - 1, "content": "x"}, nil); code != 409 {
		t.Fatalf("stale version: %d", code)
	}
	// The reviewer edits the text and picks the account the publisher will post to.
	var edited content.Post
	if code := e.do("PATCH", "/posts/"+threads.ID.String(), map[string]any{"version": threads.Version, "content": "Edited by a human.",
		"accounts": []string{"th1"}}, &edited); code != 200 {
		t.Fatalf("edit: %d", code)
	}

	// --- approve -> publish ---
	if code := e.do("POST", "/posts/"+threads.ID.String()+"/approve", map[string]any{}, nil); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	e.worker.Tick(ctx)
	var got content.Post
	e.do("GET", "/posts/"+threads.ID.String(), nil, &got)
	if got.Status != content.StatusPublished || got.ExternalPostIDs[mcppublisher.PostIDKey] == "" || got.PublishedAt == nil {
		t.Fatalf("threads post not published: %+v", got)
	}
	if posts := e.mock.Posts(); len(posts) != 1 || !strings.Contains(posts[0].Text(), "Edited by a human.") ||
		strings.Join(posts[0].Request.Accounts, ",") != "th1" {
		t.Fatalf("the publisher did not receive the edited post for the picked account: %+v", posts)
	}
	var audit []content.AuditLog
	e.do("GET", "/posts/"+threads.ID.String()+"/audit", nil, &audit)
	var actions []string
	for _, a := range audit {
		actions = append(actions, string(a.Action))
	}
	if want := "GENERATED,SUBMITTED,EDITED,APPROVED,QUEUED,DISPATCHED,PUBLISHED"; strings.Join(actions, ",") != want {
		t.Fatalf("audit trail = %s, want %s", strings.Join(actions, ","), want)
	}
	if !strings.Contains(string(audit[2].PayloadDiff), "Edited by a human.") {
		t.Errorf("edit diff not recorded: %s", audit[2].PayloadDiff)
	}

	// --- failure + manual retry ---
	// Instagram needs an image and an account before the connector lets the post through.
	if code := e.do("PATCH", "/posts/"+insta.ID.String(), map[string]any{"version": insta.Version,
		"accounts": []string{"ig1"}, "media_urls": []string{"https://cdn.example.com/a.png"}}, nil); code != 200 {
		t.Fatalf("edit instagram: %d", code)
	}
	e.mock.Fail.Store(true)
	e.do("POST", "/posts/"+insta.ID.String()+"/approve", map[string]any{}, nil)
	e.worker.Tick(ctx)
	e.do("GET", "/posts/"+insta.ID.String(), nil, &got)
	if got.Status != content.StatusFailed || !strings.Contains(got.ErrorMessage, "simulated rate limit") {
		t.Fatalf("expected FAILED with reason: %+v", got)
	}
	e.mock.Fail.Store(false)
	if code := e.do("POST", "/posts/"+insta.ID.String()+"/retry", nil, nil); code != 200 {
		t.Fatalf("retry: %d", code)
	}
	e.worker.Tick(ctx)
	e.do("GET", "/posts/"+insta.ID.String(), nil, &got)
	if got.Status != content.StatusPublished || got.ErrorMessage != "" || got.Attempts != 2 {
		t.Fatalf("retry did not publish: %+v", got)
	}

	// --- published posts are immutable ---
	if code := e.do("PATCH", "/posts/"+insta.ID.String(), map[string]any{"version": got.Version, "content": "x"}, nil); code != 400 {
		t.Fatalf("editing a published post: %d", code)
	}

	// --- stats ---
	var stats struct {
		PostsByStatus map[string]int `json:"posts_by_status"`
	}
	e.do("GET", "/stats", nil, &stats)
	if stats.PostsByStatus["PUBLISHED"] != 2 {
		t.Fatalf("stats: %+v", stats)
	}
}

func TestCrashedWorkerLeaseFailsInsteadOfDoublePosting(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var post content.Post
	if code := e.do("POST", "/posts", map[string]any{"content": "manual", "platforms": []string{"threads"}, "submit": true}, &post); code != 201 {
		t.Fatalf("create: %d", code)
	}
	if post.Status != content.StatusPendingReview {
		t.Fatalf("status %s", post.Status)
	}
	e.do("POST", "/posts/"+post.ID.String()+"/approve", map[string]any{}, nil)
	if _, err := e.cs.QueueApproved(ctx); err != nil {
		t.Fatal(err)
	}
	claimed, err := e.cs.ClaimQueued(ctx, time.Minute, 5)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v %d", err, len(claimed))
	}
	// Simulate the worker dying mid-publish: the lease expires.
	if _, err := e.pool.Exec(ctx, "UPDATE posts SET lease_until = NOW() - INTERVAL '1 second' WHERE id=$1", post.ID); err != nil {
		t.Fatal(err)
	}
	e.worker.Tick(ctx)
	got, _ := e.cs.GetPost(ctx, post.ID)
	if got.Status != content.StatusFailed || !strings.Contains(got.ErrorMessage, "check the platform") {
		t.Fatalf("expired lease should fail the post: %+v", got)
	}
	if len(e.mock.Posts()) != 0 {
		t.Fatal("expired lease must not re-publish automatically")
	}
}

func TestPublishWithoutPublisherFails(t *testing.T) {
	e := setup(t)
	var post content.Post
	e.do("POST", "/posts", map[string]any{"content": "hi", "platforms": []string{"threads"}, "submit": true}, &post)
	e.do("POST", "/posts/"+post.ID.String()+"/approve", map[string]any{}, nil)
	e.worker.Tick(context.Background())
	got, _ := e.cs.GetPost(context.Background(), post.ID)
	if got.Status != content.StatusFailed || !strings.Contains(got.ErrorMessage, "no active publisher") {
		t.Fatalf("got %+v", got)
	}
}

func TestMetaAndUI(t *testing.T) {
	e := setup(t)
	var meta struct {
		ConnectorTypes []connector.Spec `json:"connector_types"`
		Transitions    map[string][]string
	}
	e.do("GET", "/meta", nil, &meta)
	types := map[string]bool{}
	for _, s := range meta.ConnectorTypes {
		types[s.Type] = true
	}
	for _, want := range []string{"mcp_publisher", "rss", "imap", "searxng", "tavily", "openai", "anthropic", "smtp"} {
		if !types[want] {
			t.Errorf("connector type %s not registered", want)
		}
	}
	if len(meta.Transitions["PUBLISHED"]) != 0 {
		t.Errorf("PUBLISHED must be terminal")
	}
	resp, err := http.Get(e.api.URL + "/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("UI root: %v %v", err, resp)
	}
	resp.Body.Close()
}

func TestSecretKeyRotation(t *testing.T) {
	e := setup(t) // store sealed with the all-zero test key
	ctx := context.Background()
	oldKey := make([]byte, 32)
	newKey := bytes.Repeat([]byte{7}, 32)

	c, err := e.store.CreateConnector(ctx, "Publisher", "mcp_publisher", true, map[string]any{
		"endpoint_url": "http://x/mcp", "auth_token": "pub-secret"})
	if err != nil {
		t.Fatal(err)
	}
	withKeys := func(primary []byte, previous ...[]byte) *store.Store {
		box, err := secret.New(primary, previous...)
		if err != nil {
			t.Fatal(err)
		}
		return store.New(e.pool, box)
	}

	// Step 1: new primary + old previous key -> everything still readable.
	rotating := withKeys(newKey, oldKey)
	if _, cfg, err := rotating.ConnectorSettings(ctx, c.ID); err != nil || cfg.String("auth_token") != "pub-secret" {
		t.Fatalf("previous key should still decrypt: %v %v", cfg, err)
	}

	// Dry run reports without writing.
	rep, err := rotating.ReencryptConnectorSecrets(ctx, true)
	if err != nil || rep.Total != 1 || rep.Reencrypted != 1 {
		t.Fatalf("dry run: %+v %v", rep, err)
	}
	if _, _, err := withKeys(newKey).ConnectorSettings(ctx, c.ID); err == nil {
		t.Fatal("dry run must not re-encrypt")
	}

	// A wrong key set aborts without touching anything.
	if _, err := withKeys(bytes.Repeat([]byte{9}, 32)).ReencryptConnectorSecrets(ctx, false); err == nil ||
		!strings.Contains(err.Error(), "Publisher") {
		t.Fatalf("undecryptable connector should abort: %v", err)
	}
	if _, cfg, _ := withKeys(oldKey).ConnectorSettings(ctx, c.ID); cfg.String("auth_token") != "pub-secret" {
		t.Fatal("aborted rotation must leave data unchanged")
	}

	// Step 2: rotate for real.
	if rep, err := rotating.ReencryptConnectorSecrets(ctx, false); err != nil || rep.Reencrypted != 1 {
		t.Fatalf("rotate: %+v %v", rep, err)
	}
	// Step 3: only the new key is needed now; the old one no longer works.
	if _, cfg, err := withKeys(newKey).ConnectorSettings(ctx, c.ID); err != nil || cfg.String("auth_token") != "pub-secret" {
		t.Fatalf("new key should decrypt after rotation: %v %v", cfg, err)
	}
	if _, _, err := withKeys(oldKey).ConnectorSettings(ctx, c.ID); err == nil {
		t.Fatal("old key should no longer decrypt")
	}
	if rep, err := rotating.ReencryptConnectorSecrets(ctx, false); err != nil || rep.AlreadyPrimary != 1 || rep.Reencrypted != 0 {
		t.Fatalf("second rotation should be a no-op: %+v %v", rep, err)
	}
}

func TestRejectWithoutReason(t *testing.T) {
	e := setup(t)
	var a, b content.Post
	e.do("POST", "/posts", map[string]any{"content": "one", "platforms": []string{"threads"}, "submit": true}, &a)
	e.do("POST", "/posts", map[string]any{"content": "two", "platforms": []string{"threads"}, "submit": true}, &b)
	var got content.Post
	if code := e.do("POST", "/posts/"+a.ID.String()+"/reject", nil, &got); code != 200 || got.Status != content.StatusRejected || got.ReviewNote != "" {
		t.Fatalf("reject with no body: %d %s %q", code, got.Status, got.ReviewNote)
	}
	if code := e.do("POST", "/posts/"+b.ID.String()+"/reject", map[string]any{"reason": " off-brand "}, &got); code != 200 || got.ReviewNote != "off-brand" {
		t.Fatalf("reject with reason: %d %q", code, got.ReviewNote)
	}
}

// apiHandler is a second front end over the same server, after tweak (for routes setup leaves off).
func (e *env) apiHandler(tweak func(*api.Server)) http.Handler {
	s := *e.server
	tweak(&s)
	return s.Handler()
}
