package events

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
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

// An in-process broker hands the org along; For lets a subscriber keep its own org's events and the global ones.
func TestPublishCarriesOrg(t *testing.T) {
	b := NewBroker()
	ch, unsub := b.Subscribe()
	defer unsub()
	mine, other := uuid.New(), uuid.New()
	b.Publish(mine, "task.updated", map[string]any{"id": "a"})
	b.Publish(other, "task.updated", map[string]any{"id": "b"})
	b.Publish(uuid.Nil, "connector.updated", map[string]any{"id": "c"})
	var got []string
	for range 3 {
		ev := <-ch
		if ev.For(mine.String()) {
			got = append(got, ev.Name+"/"+ev.Org)
		}
		if !ev.For("") {
			t.Fatalf("an unfiltered subscriber must get every event, not %+v", ev)
		}
	}
	want := []string{"task.updated/" + mine.String(), "connector.updated/"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}
