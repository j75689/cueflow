package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/j75689/cueflow/internal/content"
	"github.com/j75689/cueflow/internal/store"
	storepg "github.com/j75689/cueflow/internal/store/pg"
)

// Store runs the content workflow's queries on the core store's pool. Methods of the core store
// are reachable through the embedded pointer.
type Store struct {
	*storepg.Store
}

var _ content.Store = (*Store)(nil)

func New(s *storepg.Store) *Store { return &Store{Store: s} }

func init() {
	content.NewStore = func(s store.Backend) content.Store { return New(s.(*storepg.Store)) }
}

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func asPg(err error, target **pgconn.PgError) bool { return errors.As(err, target) }
