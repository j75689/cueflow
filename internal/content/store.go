package content

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/j75689/cueflow/internal/store"
)

// Store runs the content workflow's queries on the core store's pool. Methods of the core store
// are reachable through the embedded pointer.
type Store struct {
	*store.Store
}

func NewStore(s *store.Store) *Store { return &Store{Store: s} }

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func asPg(err error, target **pgconn.PgError) bool { return errors.As(err, target) }
