package main

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	storesqlite "github.com/j75689/cueflow/internal/store/sqlite"
)

// copy-db copies the tables the baseline creates: a later migration that adds a table must add it to
// the copy too, or copy-db would report its rows as PostgreSQL only and leave them behind.
func TestCopyDBCopiesEveryTable(t *testing.T) {
	ctx := context.Background()
	s, err := storesqlite.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "cueflow.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := storesqlite.Migrate(ctx, s); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Reader().Query(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	var migrated []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		migrated = append(migrated, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	tables, err := baselineTables()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(migrated)
	if copied := slices.Sorted(slices.Values(tables)); !slices.Equal(copied, migrated) {
		t.Errorf("copy-db copies %v, a migrated file has %v", copied, migrated)
	}
}
