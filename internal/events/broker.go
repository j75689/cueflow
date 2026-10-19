// Package events is a pub/sub used to push live updates to dashboard clients (SSE). By default it is
// in-process; Attach makes it fan out through PostgreSQL LISTEN/NOTIFY so every instance sees every event.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel is the NOTIFY channel events travel on.
const Channel = "cueflow_events"

type Event struct {
	Name string `json:"name"`
	// Org is the org the change belongs to; empty for global changes (connectors, runners, orgs), which
	// every subscriber gets.
	Org  string `json:"org,omitempty"`
	Data any    `json:"data"`
}

// For reports whether a subscriber watching org should get ev: its own org's events and global ones.
// An empty org watches every org.
func (ev Event) For(org string) bool {
	return org == "" || ev.Org == "" || ev.Org == org
}

type Broker struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
	pool *pgxpool.Pool
	log  *slog.Logger
}

func NewBroker() *Broker { return &Broker{subs: map[chan Event]struct{}{}} }

// Attach routes Publish through NOTIFY and delivers what Run hears. Call before Run.
func (b *Broker) Attach(pool *pgxpool.Pool, log *slog.Logger) {
	b.pool = pool
	b.log = log.With("component", "events")
}

// Subscribe returns a channel of events and a cancel func that must be called.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Publish sends an org's event: its subscribers and the unfiltered ones get it, with org_id added to a map
// payload. An unknown org (uuid.Nil, e.g. a lookup that failed) drops the event rather than sending it to every
// org as a global one; the clients catch up on their next refetch.
func (b *Broker) Publish(org uuid.UUID, name string, data any) {
	if b == nil || org == uuid.Nil {
		return
	}
	if m, ok := data.(map[string]any); ok {
		withOrg := make(map[string]any, len(m)+1)
		for k, v := range m {
			withOrg[k] = v
		}
		withOrg["org_id"] = org.String()
		data = withOrg
	}
	b.send(Event{Name: name, Org: org.String(), Data: data})
}

// PublishGlobal sends a change that belongs to no org (connectors, orgs, runners) to every subscriber.
func (b *Broker) PublishGlobal(name string, data any) {
	if b == nil {
		return
	}
	b.send(Event{Name: name, Data: data})
}

// send fans out without blocking; slow subscribers drop events (clients refetch on reconnect).
func (b *Broker) send(ev Event) {
	if b.pool != nil {
		payload, err := json.Marshal(ev)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err = b.pool.Exec(ctx, "SELECT pg_notify($1, $2)", Channel, string(payload)); err == nil {
				return
			}
		}
		b.log.Warn("notify failed; delivering locally", "err", err)
	}
	b.deliver(ev)
}

func (b *Broker) deliver(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Run listens for notifications until ctx ends, reconnecting on failure. It is a no-op for an
// in-process broker.
func (b *Broker) Run(ctx context.Context) {
	if b.pool == nil {
		return
	}
	for ctx.Err() == nil {
		if err := b.listen(ctx); err != nil && ctx.Err() == nil {
			b.log.Warn("event listener stopped; reconnecting", "err", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func (b *Broker) listen(ctx context.Context) error {
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		return err
	}
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			// A broken connection must not go back to the pool.
			_ = conn.Conn().Close(context.Background())
			return err
		}
		var ev Event
		if json.Unmarshal([]byte(n.Payload), &ev) == nil {
			b.deliver(ev)
		}
	}
}
