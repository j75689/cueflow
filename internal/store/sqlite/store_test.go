package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "cueflow.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := Migrate(ctx, s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Migrating a file a second time applies nothing: it keeps the version and the rows written in between
// (a reopened single-mode install migrates on every start).
func TestMigrateReopen(t *testing.T) {
	ctx := context.Background()
	url := "sqlite://" + filepath.Join(t.TempDir(), "cueflow.db")
	for i := range 2 {
		s, err := Open(ctx, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := Migrate(ctx, s); err != nil {
			s.Close()
			t.Fatalf("migrate #%d: %v", i+1, err)
		}
		if i == 0 {
			if _, err := s.Writer().Exec(ctx, "INSERT INTO clock (name, next_run_at) VALUES ('kept', 'x')"); err != nil {
				t.Fatal(err)
			}
			s.Close()
			continue
		}
		defer s.Close()
		var version string
		var kept int
		if err := s.Reader().QueryRow(ctx, "SELECT max(version) FROM schema_migrations").Scan(&version); err != nil || version != "0027_baseline" {
			t.Errorf("version after reopening: %q %v, want 0027_baseline", version, err)
		}
		if err := s.Reader().QueryRow(ctx, "SELECT count(*) FROM clock WHERE name = 'kept'").Scan(&kept); err != nil || kept != 1 {
			t.Errorf("row written before reopening: %d %v", kept, err)
		}
	}
}

// A panic inside Tx must not leave the one write connection in its transaction.
func TestTxRollsBackOnPanic(t *testing.T) {
	s := openTest(t)
	func() {
		defer func() { _ = recover() }()
		_ = s.Tx(context.Background(), func(q Querier) error {
			if _, err := q.Exec(context.Background(), "INSERT INTO clock (name, next_run_at) VALUES ('a', 'x')"); err != nil {
				t.Fatal(err)
			}
			panic("boom")
		})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Tx(ctx, func(q Querier) error {
		_, err := q.Exec(ctx, "INSERT INTO clock (name, next_run_at) VALUES ('b', 'x')")
		return err
	}); err != nil {
		t.Fatalf("write after a panic: %v", err)
	}
	var n int
	if err := s.Reader().QueryRow(ctx, "SELECT count(*) FROM clock WHERE name IN ('a', 'b')").Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows: %d %v (the panicking insert must be rolled back)", n, err)
	}
}

// The text primary keys refuse NULL, as in PostgreSQL.
func TestPrimaryKeysRefuseNull(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, q := range []string{
		"INSERT INTO clock (name, next_run_at) VALUES (NULL, 'x')",
		"INSERT INTO orgs (id, slug, name) VALUES (NULL, 'n', 'n')",
		"INSERT INTO schema_migrations (version, applied_at) VALUES (NULL, 'x')",
	} {
		if _, err := s.Writer().Exec(ctx, q); err == nil || !strings.Contains(err.Error(), "NOT NULL") {
			t.Errorf("%s: %v", q, err)
		}
	}
}

// The max(seq) + 1 on insert reads an index, not the whole table.
func TestNextSeqUsesIndex(t *testing.T) {
	s := openTest(t)
	for _, table := range []string{"task_messages", "deliverable_audit_logs"} {
		var id, parent, unused int
		var plan string
		if err := s.Reader().QueryRow(context.Background(), "EXPLAIN QUERY PLAN SELECT COALESCE(max(seq), 0) + 1 FROM "+table).
			Scan(&id, &parent, &unused, &plan); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan, "idx_"+table+"_seq") {
			t.Errorf("%s: %s", table, plan)
		}
	}
}
