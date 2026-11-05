package content

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/store"
)

// Store is the content workflow's persistence: the core backend plus the source items. The
// implementation lives with the backend (content/pg) and embeds the core store.
type Store interface {
	store.Backend
	InsertItems(ctx context.Context, connectorID uuid.UUID, items []connector.Item) (int, error)
	UnconsumedItems(ctx context.Context, connectorIDs []uuid.UUID, since time.Time, limit int) ([]*SourceItem, error)
	MarkItemsConsumed(ctx context.Context, ids []uuid.UUID) error
	ListItems(ctx context.Context, connectorID uuid.UUID, limit int) ([]*SourceItem, error)
	SearchItems(ctx context.Context, connectorIDs []uuid.UUID, query string, since time.Time, limit int) ([]*SourceItem, error)
}

// NewStore returns the content store over the core backend s, for code that holds only the core
// (the agents' tools). The backend's package sets it (content/pg).
var NewStore func(s store.Backend) Store

type SourceItem struct {
	ID          uuid.UUID  `json:"id"`
	ConnectorID uuid.UUID  `json:"connector_id"`
	ExternalID  string     `json:"external_id"`
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	Author      string     `json:"author"`
	Content     string     `json:"content"`
	PublishedAt *time.Time `json:"published_at"`
	FetchedAt   time.Time  `json:"fetched_at"`
	ConsumedAt  *time.Time `json:"consumed_at"`
}
