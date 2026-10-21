// Package events is a pub/sub used to push live updates to dashboard clients (SSE). By default it is
// in-process; Use makes it fan out through a Transport (PostgreSQL LISTEN/NOTIFY, or Google Pub/Sub
// in package events/pubsub) so every instance sees every event.
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
	mu        sync.Mutex
	subs      map[chan Event]struct{}
	transport Transport
	log       *slog.Logger
}

func NewBroker() *Broker { return &Broker{subs: map[chan Event]struct{}{}} }

// Use routes Publish through t and delivers what Run hears from it. Call before Run.
func (b *Broker) Use(t Transport, log *slog.Logger) {
	b.transport = t
	b.log = log.With("component", "events")
}

// Attach is Use with PostgreSQL LISTEN/NOTIFY on the pool.
func (b *Broker) Attach(pool *pgxpool.Pool, log *slog.Logger) { b.Use(&PG{Pool: pool}, log) }

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
	if b == nil {
		return
	}
	if org == uuid.Nil {
		if b.log != nil {
			b.log.Debug("event dropped: no org", "name", name)
		}
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
	if b.transport != nil {
		payload, err := json.Marshal(ev)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
			defer cancel()
			if err = b.transport.Publish(ctx, payload); err == nil {
				return
			}
		}
		b.log.Warn("publish failed; delivering locally", "err", err)
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

// publishTimeout bounds how long Publish may hold up the request that caused the event.
const publishTimeout = 3 * time.Second

// Run receives events from the transport until ctx ends, reconnecting on failure. It is a no-op for
// an in-process broker.
func (b *Broker) Run(ctx context.Context) {
	if b.transport == nil {
		return
	}
	for ctx.Err() == nil {
		err := b.transport.Receive(ctx, func(payload []byte) {
			var ev Event
			if json.Unmarshal(payload, &ev) == nil {
				b.deliver(ev)
			}
		})
		if err != nil && ctx.Err() == nil {
			b.log.Warn("event receiver stopped; reconnecting", "err", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}
