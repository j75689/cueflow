package pg

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/j75689/cueflow/internal/events"
)

// Two brokers sharing one database: an event published on A reaches B's subscribers.
func TestNotifyAcrossInstances(t *testing.T) {
	url := os.Getenv("CUEFLOW_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CUEFLOW_TEST_DATABASE_URL not set")
	}
	// With a session URL (the transaction-pooler setup) the pool publishes through the pooler and
	// LISTEN gets a session connection of its own. CUEFLOW_TEST_DATABASE_SESSION_URL is that session
	// URL when the URL above is a real transaction pooler; otherwise the same database stands in.
	session := os.Getenv("CUEFLOW_TEST_DATABASE_SESSION_URL")
	t.Run("pool listens", func(t *testing.T) {
		if session != "" {
			t.Skip("LISTEN through a transaction pooler hears nothing; that is what ListenURL is for")
		}
		notifyAcross(t, url, "")
	})
	if session == "" {
		session = url
	}
	t.Run("session url listens", func(t *testing.T) { notifyAcross(t, url, session) })
}

func notifyAcross(t *testing.T, url, listenURL string) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if listenURL != "" { // as db.OpenWith does for a transaction pooler
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe
		cfg.ConnConfig.StatementCacheCapacity = 0
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); pool.Close() }() // listeners hold connections until ctx ends
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, b := events.NewBroker(), events.NewBroker()
	a.Use(&Transport{Pool: pool, ListenURL: listenURL}, log)
	b.Use(&Transport{Pool: pool, ListenURL: listenURL}, log)
	go a.Run(ctx)
	go b.Run(ctx)
	ch, unsub := b.Subscribe()
	defer unsub()
	time.Sleep(500 * time.Millisecond) // let LISTEN register
	org := uuid.New()
	a.Publish(org, "task.updated", map[string]any{"id": "x"})
	select {
	case ev := <-ch:
		if ev.Name != "task.updated" || ev.Org != org.String() {
			t.Fatalf("got %q for org %q", ev.Name, ev.Org)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event did not arrive")
	}
}
