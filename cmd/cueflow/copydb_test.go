package main

import (
	"context"
	"errors"
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

// Each table's write transaction refuses a target with data before it deletes or inserts anything,
// even if checkTarget let the target through: the source isn't read (tx is nil) and the orgs stay.
func TestCopyDBWontWriteIntoData(t *testing.T) {
	ctx := context.Background()
	s, err := storesqlite.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "live.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := storesqlite.Migrate(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Writer().Exec(ctx, `INSERT INTO orgs (name, slug) VALUES ('Mine', 'mine')`); err != nil {
		t.Fatal(err)
	}
	tables, err := baselineTables()
	if err != nil {
		t.Fatal(err)
	}
	for i, table := range tables[:2] { // connectors, the first write, then orgs, the one that deletes
		if _, err := (tablePlan{table: table}).copy(ctx, nil, s, tables[i:]); !errors.Is(err, errTargetNotEmpty) {
			t.Errorf("copy %s into a target with data: %v, want %v", table, err, errTargetNotEmpty)
		}
	}
	var orgs int
	if err := s.Reader().QueryRow(ctx, `SELECT count(*) FROM orgs`).Scan(&orgs); err != nil || orgs != 2 {
		t.Errorf("orgs after the refused copies: %d %v, want 2", orgs, err)
	}
}
