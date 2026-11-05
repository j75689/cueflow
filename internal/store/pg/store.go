// Package pg is the PostgreSQL backend of package store: the pool, the embedded migrations and
// every query.
package pg

import (
	"context"
	"errors"

	"github.com/j75689/cueflow/internal/blob"
	"github.com/j75689/cueflow/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/secret"
)

type Store struct {
	pool *pgxpool.Pool
	box  *secret.Box
	// blobs holds new media and task-file bytes; nil keeps them in the database.
	blobs blob.Store
	// mediaUse says which media a module still uses; nil means none.
	mediaUse store.MediaUse
}

var _ store.Backend = (*Store)(nil)

func New(pool *pgxpool.Pool, box *secret.Box) *Store {
	return &Store{pool: pool, box: box}
}

// SetBlobs makes new media bytes go to b instead of the database. Existing rows keep working.
func (s *Store) SetBlobs(b blob.Store) { s.blobs = b }

// SetMediaUse makes media u reports in use undeletable (DeleteMedia, PruneMedia).
func (s *Store) SetMediaUse(u store.MediaUse) { s.mediaUse = u }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Close closes the pool.
func (s *Store) Close() { s.pool.Close() }

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// NotFound maps pgx.ErrNoRows to domain.ErrNotFound.
func NotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

func asPg(err error, target **pgconn.PgError) bool { return errors.As(err, target) }

// CountConnectors counts the connectors for the dashboard (they are global, not per org).
func (s *Store) CountConnectors(ctx context.Context) (total, active int, err error) {
	err = s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM connectors), (SELECT count(*) FROM connectors WHERE is_active)`).Scan(&total, &active)
	return
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
