// Package store is the PostgreSQL persistence layer.
package store

import (
	"context"
	"errors"
	"github.com/j75689/cueflow/internal/blob"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/secret"
)

type Store struct {
	pool *pgxpool.Pool
	box  *secret.Box
	// blobs holds new media bytes; nil keeps them in the database.
	blobs blob.Store
}

func New(pool *pgxpool.Pool, box *secret.Box) *Store {
	return &Store{pool: pool, box: box}
}

// SetBlobs makes new media bytes go to b instead of the database. Existing rows keep working.
func (s *Store) SetBlobs(b blob.Store) { s.blobs = b }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

// ValidationError is a caller error (bad input) as opposed to a storage failure.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

func asPg(err error, target **pgconn.PgError) bool { return errors.As(err, target) }
