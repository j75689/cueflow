// Package e2e exercises CueFlow against a real PostgreSQL: agents on the board, the deliverables
// they produce, human approval through the API, and the post kind publishing through an MCP
// publisher (in-process mock), plus failure and retry.
//
// Set CUEFLOW_TEST_DATABASE_URL to a disposable database to run it (its public schema is reset).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/j75689/cueflow/internal/api"
	"github.com/j75689/cueflow/internal/connector"
	_ "github.com/j75689/cueflow/internal/connectors/all"
	"github.com/j75689/cueflow/internal/content"
	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/db"
	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/publishermock"
	"github.com/j75689/cueflow/internal/scheduler"
	"github.com/j75689/cueflow/internal/secret"
	"github.com/j75689/cueflow/internal/store"
)

type env struct {
	t         *testing.T
	pool      *pgxpool.Pool
	store     *store.Store
	cs        *content.Store
	ds        *deliverable.Store
	deliverer *deliverable.Worker
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
	module := &content.Module{Store: cs, Hub: h, Broker: broker, Logger: logger, MediaBaseURL: "http://localhost:3000"}
	module.Attach()
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

	e := &env{t: t, pool: pool, store: st, cs: cs, ds: ds, deliverer: deliverer, crew: team, sched: sched, api: apiSrv, server: server, mock: mock}
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

// apiHandler is a second front end over the same server, after tweak (for routes setup leaves off).
func (e *env) apiHandler(tweak func(*api.Server)) http.Handler {
	s := *e.server
	tweak(&s)
	return s.Handler()
}
