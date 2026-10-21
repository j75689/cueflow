package e2e

import (
	"context"
	"testing"

	"github.com/j75689/cueflow/internal/connector"
)

// InsertItems sends its inserts as one pgx batch: one Sync, so behind a transaction pooler they run
// as one implicit transaction on one server connection. A repeat fetch only counts what is new.
func TestInsertItemsBatch(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	src, err := e.store.CreateConnector(ctx, "Feed", "rss", true, map[string]any{"url": "http://example.invalid/feed"})
	if err != nil {
		t.Fatal(err)
	}
	items := []connector.Item{{ExternalID: "a", Title: "A"}, {ExternalID: "b", Title: "B"}, {ExternalID: "a", Title: "A again"}, {Title: "no id"}}
	if n, err := e.cs.InsertItems(ctx, src.ID, items); err != nil || n != 2 {
		t.Fatalf("first fetch: %d new, %v", n, err)
	}
	items = append(items, connector.Item{ExternalID: "c", Title: "C"})
	if n, err := e.cs.InsertItems(ctx, src.ID, items); err != nil || n != 1 {
		t.Fatalf("second fetch: %d new, %v", n, err)
	}
}
