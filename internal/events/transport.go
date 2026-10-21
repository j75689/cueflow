package events

import "context"

// Transport carries events between the instances of one deployment. Ordering and delivery are best
// effort: every subscriber treats an event as "refetch", and a lost one only leaves a view stale
// until its next refetch.
type Transport interface {
	// Publish hands one event to every instance, this one included.
	Publish(ctx context.Context, payload []byte) error
	// Receive calls deliver with what is published until ctx ends or the transport fails; Run
	// calls it again after a failure.
	Receive(ctx context.Context, deliver func(payload []byte)) error
}
