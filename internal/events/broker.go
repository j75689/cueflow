// Package events is a pub/sub used to push live updates to dashboard clients (SSE). By default it is
// in-process; Attach makes it fan out through PostgreSQL LISTEN/NOTIFY so every instance sees every event.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel is the NOTIFY channel events travel on.
const Channel = "cueflow_events"

type Event struct {
	Name string `json:"name"`
	Data any    `json:"data"`
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

// Publish fans out without blocking; slow subscribers drop events (clients refetch on reconnect).
func (b *Broker) Publish(name string, data any) {
	if b == nil {
		return
	}
	if b.pool != nil {
		payload, err := json.Marshal(Event{Name: name, Data: data})
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err = b.pool.Exec(ctx, "SELECT pg_notify($1, $2)", Channel, string(payload)); err == nil {
				return
			}
		}
		b.log.Warn("notify failed; delivering locally", "err", err)
	}
	b.deliver(Event{Name: name, Data: data})
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
