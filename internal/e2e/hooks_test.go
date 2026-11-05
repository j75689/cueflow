package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	storepg "github.com/j75689/cueflow/internal/store/pg"
)

// dbHooks is how a test reads and sets rows the product API doesn't reach, on whichever backend
// the suite runs. In a where map a value matches with =, a []uuid.UUID or []string matches any of
// its elements, and nil matches NULL. Times and ids are Go values (time.Now().Add(-time.Minute),
// uuid.New()), never SQL.
type dbHooks interface {
	Update(ctx context.Context, table string, set, where map[string]any) (int64, error)
	Insert(ctx context.Context, table string, row map[string]any) error
	Count(ctx context.Context, table string, where map[string]any) (int, error)
	Get(ctx context.Context, table, column string, where map[string]any, dest any) error
}

var _ dbHooks = (*storepg.TestHooks)(nil)

// pgOnly skips the test unless it runs on PostgreSQL, and returns the pool for what only
// PostgreSQL has: holding row, table and advisory locks from another transaction, pg_stat_activity.
// Call it right after setup, so a test skips before it starts workers or writes rows.
func pgOnly(t *testing.T, e *env) *pgxpool.Pool {
	t.Helper()
	h, ok := e.db.(*storepg.TestHooks)
	if !ok {
		t.Skip("needs PostgreSQL")
	}
	return h.Pool
}

// The hooks do what the converted assertions rely on: = per column, a slice as IN, nil as IS NULL, an
// empty where as every row, Update's count of changed rows, and Get's no-row error.
func TestDBHooks(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	def, _ := e.store.DefaultOrg(ctx)
	var ids []uuid.UUID
	for i := range 3 {
		id := uuid.New()
		if err := e.db.Insert(ctx, "tasks", map[string]any{"id": id, "org_id": def.ID, "number": 900 + i,
			"title": fmt.Sprint("hook ", i), "status": "TODO"}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	count := func(where map[string]any) int {
		t.Helper()
		n, err := e.db.Count(ctx, "tasks", where)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(nil); n != 3 {
		t.Fatalf("every row: %d", n)
	}
	if n := count(map[string]any{"id": ids[:2]}); n != 2 {
		t.Fatalf("ids IN: %d", n)
	}
	if n := count(map[string]any{"title": []string{"hook 0", "hook 2", "other"}}); n != 2 {
		t.Fatalf("titles IN: %d", n)
	}

	later := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	if n, err := e.db.Update(ctx, "tasks", map[string]any{"retry_at": later, "status": "DONE"}, map[string]any{"id": ids[0]}); err != nil || n != 1 {
		t.Fatalf("update: %d %v", n, err)
	}
	if n := count(map[string]any{"retry_at": nil}); n != 2 {
		t.Fatalf("retry_at IS NULL: %d", n)
	}
	if n := count(map[string]any{"status": "DONE", "id": ids[0]}); n != 1 {
		t.Fatalf("status and id: %d", n)
	}
	var at *time.Time
	if err := e.db.Get(ctx, "tasks", "retry_at", map[string]any{"id": ids[0]}, &at); err != nil || at == nil || !at.Equal(later) {
		t.Fatalf("get retry_at: %v %v, want %v", at, err, later)
	}
	if err := e.db.Get(ctx, "tasks", "retry_at", map[string]any{"id": ids[1]}, &at); err != nil || at != nil {
		t.Fatalf("get a NULL retry_at: %v %v", at, err)
	}
	if n, err := e.db.Update(ctx, "tasks", map[string]any{"retry_at": nil}, map[string]any{"status": "TODO"}); err != nil || n != 2 {
		t.Fatalf("update no-op rows: %d %v", n, err)
	}
	var title string
	if err := e.db.Get(ctx, "tasks", "title", map[string]any{"id": uuid.New()}, &title); err == nil {
		t.Fatal("get on no row should fail")
	}
}
