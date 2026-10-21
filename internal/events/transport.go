package events

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

// PG is LISTEN/NOTIFY on Channel.
type PG struct {
	Pool *pgxpool.Pool
	// ListenURL, when set, is where Receive LISTENs: a session-mode URL for a pool that goes through
	// a transaction pooler, which can't keep a LISTEN. Empty listens on a connection of the pool.
	ListenURL string
}

func (t *PG) Publish(ctx context.Context, payload []byte) error {
	_, err := t.Pool.Exec(ctx, "SELECT pg_notify($1, $2)", Channel, string(payload))
	return err
}

func (t *PG) Receive(ctx context.Context, deliver func([]byte)) error {
	var conn *pgx.Conn
	if t.ListenURL != "" {
		c, err := pgx.Connect(ctx, t.ListenURL)
		if err != nil {
			return err
		}
		defer c.Close(context.Background())
		conn = c
	} else {
		c, err := t.Pool.Acquire(ctx)
		if err != nil {
			return err
		}
		defer c.Release()
		conn = c.Conn()
	}
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		return err
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			// A broken connection must not go back to the pool.
			_ = conn.Close(context.Background())
			return err
		}
		deliver([]byte(n.Payload))
	}
}
