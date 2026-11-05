// Package sqlite is the SQLite backend of package store, for single mode: one process, one database
// file. Writes go through a pool of one connection whose transactions begin IMMEDIATE, so writers
// take turns instead of failing with SQLITE_BUSY; reads go through a pool of their own (WAL lets
// them run beside the writer). The queries follow store/pg's, rewritten where SQLite differs.
package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/j75689/cueflow/internal/blob"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/secret"
	"github.com/j75689/cueflow/internal/store"
)

type Store struct {
	// r reads; w writes, one statement or transaction at a time.
	r, w   db
	rp, wp *sql.DB
	box    *secret.Box
	// blobs holds new media and task-file bytes; nil keeps them in the database.
	blobs blob.Store
	// mediaUse says which media a module still uses; nil means none.
	mediaUse store.MediaUse
}

var _ store.Backend = (*Store)(nil)

// SetBlobs makes new media bytes go to b instead of the database. Existing rows keep working.
func (s *Store) SetBlobs(b blob.Store) { s.blobs = b }

// SetMediaUse makes media u reports in use undeletable (DeleteMedia, PruneMedia).
func (s *Store) SetMediaUse(u store.MediaUse) { s.mediaUse = u }

func (s *Store) Ping(ctx context.Context) error {
	if err := s.wp.PingContext(ctx); err != nil {
		return err
	}
	return s.rp.PingContext(ctx)
}

// Close closes both pools.
func (s *Store) Close() {
	_ = s.rp.Close()
	_ = s.wp.Close()
}

// Reader runs the module packages' reads (deliverable/sqlite, content/sqlite).
func (s *Store) Reader() Querier { return s.r }

// Writer runs the module packages' single-statement writes.
func (s *Store) Writer() Querier { return s.w }

// Tx runs fn in one write transaction, committed if fn returns nil. fn must use only q (and reads):
// the write pool has one connection, which q holds.
func (s *Store) Tx(ctx context.Context, fn func(q Querier) error) error {
	tx, err := s.wp.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // a no-op after Commit; frees the write connection if fn panics
	if err := fn(db{tx}); err != nil {
		return err
	}
	return tx.Commit()
}

// Querier runs SQL with Go values: see args and dest.
type Querier interface {
	Exec(ctx context.Context, query string, args ...any) (Result, error)
	Query(ctx context.Context, query string, args ...any) (*Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) Row
}

// Row is one result row (a *Rows positioned on its row, or a single-row query).
type Row interface {
	Scan(dest ...any) error
}

// Result is what an Exec changed.
type Result struct{ n int64 }

func (r Result) RowsAffected() int64 { return r.n }

type conn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// db is a Querier on a pool or a transaction.
type db struct{ c conn }

func (d db) Exec(ctx context.Context, query string, a ...any) (Result, error) {
	res, err := d.c.ExecContext(ctx, query, args(a)...)
	if err != nil {
		return Result{}, err
	}
	n, err := res.RowsAffected()
	return Result{n}, err
}

func (d db) Query(ctx context.Context, query string, a ...any) (*Rows, error) {
	rows, err := d.c.QueryContext(ctx, query, args(a)...)
	if err != nil {
		return nil, err
	}
	return &Rows{rows}, nil
}

func (d db) QueryRow(ctx context.Context, query string, a ...any) Row {
	return row{d.c.QueryRowContext(ctx, query, args(a)...)}
}

// Rows is database/sql's rows, scanning through dest.
type Rows struct{ *sql.Rows }

func (r *Rows) Scan(dest ...any) error { return r.Rows.Scan(dests(dest)...) }

type row struct{ r *sql.Row }

func (r row) Scan(dest ...any) error { return r.r.Scan(dests(dest)...) }

// NotFound maps sql.ErrNoRows to domain.ErrNotFound.
func NotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

// CountConnectors counts the connectors for the dashboard (they are global, not per org).
func (s *Store) CountConnectors(ctx context.Context) (total, active int, err error) {
	err = s.r.QueryRow(ctx, `SELECT (SELECT count(*) FROM connectors), (SELECT count(*) FROM connectors WHERE is_active)`).Scan(&total, &active)
	return
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
