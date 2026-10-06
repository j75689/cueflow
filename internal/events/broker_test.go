package events

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Two brokers sharing one database: an event published on A reaches B's subscribers.
func TestNotifyAcrossInstances(t *testing.T) {
	url := os.Getenv("CUEFLOW_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CUEFLOW_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); pool.Close() }() // listeners hold connections until ctx ends
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, b := NewBroker(), NewBroker()
	a.Attach(pool, log)
	b.Attach(pool, log)
	go a.Run(ctx)
	go b.Run(ctx)
	ch, unsub := b.Subscribe()
	defer unsub()
	time.Sleep(500 * time.Millisecond) // let LISTEN register
	a.Publish("task.updated", map[string]any{"id": "x"})
	select {
	case ev := <-ch:
		if ev.Name != "task.updated" {
			t.Fatalf("got %q", ev.Name)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event did not arrive")
	}
}
