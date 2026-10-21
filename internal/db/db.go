// Package db opens the PostgreSQL pool and applies embedded migrations.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	return OpenWith(ctx, url, Options{})
}

// Options tune the pool.
type Options struct {
	// MaxConns caps the pool (0 keeps the driver default). A small cap lets several cloud instances
	// share a database with a low connection limit.
	MaxConns int
	// TxPooler opens the pool for a transaction-mode pooler (Supavisor on 6543, PgBouncer in
	// transaction mode), which lends a server connection only until the end of each transaction, so
	// no named prepared statement survives from one query to the next. Queries run in
	// QueryExecModeCacheDescribe: the parameter and result types of each statement are learned once
	// (through the unnamed statement) and cached on the client, and every execution is a single
	// Parse/Bind/Execute round trip that needs nothing from an earlier one. QueryExecModeExec would
	// also be safe but sends untyped text parameters, which turns a []byte meant for jsonb into
	// bytea; DescribeExec needs its two round trips on the same server connection. A migration that
	// changes a cached statement's types makes its next run fail once, as with the default statement
	// cache. Session-only work (LISTEN, the migration lock) goes through the session URL instead.
	TxPooler bool
}

// OpenWith opens the pool with the given options.
func OpenWith(ctx context.Context, url string, o Options) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	if o.MaxConns > 0 {
		cfg.MaxConns = int32(o.MaxConns)
	}
	if o.TxPooler {
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe
		cfg.ConnConfig.StatementCacheCapacity = 0
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// migrationLockKey serializes concurrent migrators (two containers starting at once).
const migrationLockKey = 0x637565666c6f77 // "cueflow"

// Migrate applies every embedded migration not yet recorded in schema_migrations, each in its
// own transaction, on a connection of the pool.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	return migrate(ctx, conn.Conn())
}

// MigrateSession is Migrate on a short-lived connection of its own to sessionURL: the advisory lock
// that keeps two starting instances from migrating at once belongs to a session, which a
// transaction pooler doesn't keep.
func MigrateSession(ctx context.Context, sessionURL string) error {
	conn, err := pgx.Connect(ctx, sessionURL)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	return migrate(ctx, conn)
}

func migrate(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockKey) //nolint:errcheck

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`); err != nil {
		return err
	}

	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(body)); err != nil {
				return fmt.Errorf("migration %s: %w", version, err)
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, version)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}
