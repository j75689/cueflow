// Package events is an in-process pub/sub used to push live updates to dashboard clients (SSE).
package events

import (
	"sync"
)

type Event struct {
	Name string `json:"name"`
	Data any    `json:"data"`
}

type Broker struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBroker() *Broker { return &Broker{subs: map[chan Event]struct{}{}} }

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
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- Event{Name: name, Data: data}:
		default:
		}
	}
}
