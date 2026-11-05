package pg

import (
	"context"
	"errors"
	"fmt"

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

// init serves content.NewStore for the PostgreSQL store and hands any other backend to the
// constructor registered before it.
func init() {
	next := content.NewStore
	content.NewStore = func(s store.Backend) content.Store {
		if core, ok := s.(*storepg.Store); ok {
			return New(core)
		}
		if next == nil {
			panic(fmt.Sprintf("no content store for %T", s))
		}
		return next(s)
	}
}

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func asPg(err error, target **pgconn.PgError) bool { return errors.As(err, target) }
