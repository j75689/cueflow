package store_test

import (
	"io/fs"
	"slices"
	"strings"
	"testing"

	storepg "github.com/j75689/cueflow/internal/store/pg"
	storesqlite "github.com/j75689/cueflow/internal/store/sqlite"
)

// sqliteBaseline is where SQLite's schema starts: PostgreSQL's as of its migration of that number.
const sqliteBaseline = "0027"

// Every PostgreSQL migration from the SQLite baseline on has a SQLite twin with the same NNNN_
// prefix, and the other way round, so neither backend gets a schema change the other lacks.
func TestMigrationsComeInPairs(t *testing.T) {
	pg := versions(t, storepg.Migrations)
	lite := versions(t, storesqlite.Migrations)
	if len(lite) == 0 || lite[0] != sqliteBaseline || !slices.Contains(names(t, storesqlite.Migrations), sqliteBaseline+"_baseline.sql") {
		t.Fatalf("SQLite's first migration must be %s_baseline.sql, got %v", sqliteBaseline, names(t, storesqlite.Migrations))
	}
	for _, v := range pg {
		if v >= sqliteBaseline && !slices.Contains(lite, v) {
			t.Errorf("migration %s is in store/pg/migrations but not in store/sqlite/migrations", v)
		}
	}
	for _, v := range lite {
		if !slices.Contains(pg, v) {
			t.Errorf("migration %s is in store/sqlite/migrations but not in store/pg/migrations", v)
		}
	}
}

// versions are the NNNN prefixes of the migration files in fsys, ascending.
func versions(t *testing.T, fsys fs.FS) []string {
	var out []string
	for _, name := range names(t, fsys) {
		v, _, _ := strings.Cut(name, "_")
		out = append(out, v)
	}
	return out
}

func names(t *testing.T, fsys fs.FS) []string {
	t.Helper()
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	return names
}
