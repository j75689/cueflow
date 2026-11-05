package sqlite

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations are the embedded migration files, by name (NNNN_name.sql).
var Migrations, _ = fs.Sub(migrations, "migrations")

// Migrate applies every embedded migration not yet recorded in schema_migrations, each in its own
// transaction on the write connection. It is store/pg's loop without the advisory lock: one process
// owns the file, and its write connection already lets one writer in at a time.
func Migrate(ctx context.Context, s *Store) error {
	if _, err := s.w.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
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
		if err := s.w.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		if err := s.Tx(ctx, func(tx Querier) error {
			if _, err := tx.Exec(ctx, string(body)); err != nil {
				return fmt.Errorf("migration %s: %w", version, err)
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES ($1, $now)`, version)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}
