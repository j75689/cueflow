// Package pg is the events.Transport over PostgreSQL LISTEN/NOTIFY, the default: every instance
// LISTENs on Channel and a publish is a pg_notify on it.
package pg

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/j75689/cueflow/internal/events"
)

// Channel is the NOTIFY channel events travel on.
const Channel = "cueflow_events"

var _ events.Transport = (*Transport)(nil)

// Transport is LISTEN/NOTIFY on Channel.
type Transport struct {
	Pool *pgxpool.Pool
	// ListenURL, when set, is where Receive LISTENs: a session-mode URL for a pool that goes through
	// a transaction pooler, which can't keep a LISTEN. Empty listens on a connection of the pool.
	ListenURL string
}

func (t *Transport) Publish(ctx context.Context, payload []byte) error {
	_, err := t.Pool.Exec(ctx, "SELECT pg_notify($1, $2)", Channel, string(payload))
	return err
}

func (t *Transport) Receive(ctx context.Context, deliver func([]byte)) error {
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
