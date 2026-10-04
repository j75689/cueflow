package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/j75689/cueflow/internal/connector"
)

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

const itemColumns = `id, connector_id, external_id, title, url, author, content, published_at, fetched_at, consumed_at`

func scanItem(row pgx.Row) (*SourceItem, error) {
	var it SourceItem
	err := row.Scan(&it.ID, &it.ConnectorID, &it.ExternalID, &it.Title, &it.URL, &it.Author, &it.Content,
		&it.PublishedAt, &it.FetchedAt, &it.ConsumedAt)
	return &it, err
}

// InsertItems stores newly fetched items, skipping ones already seen (delta fetching), and returns
// how many were new.
func (s *Store) InsertItems(ctx context.Context, connectorID uuid.UUID, items []connector.Item) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	batch := &pgx.Batch{}
	for _, it := range items {
		if it.ExternalID == "" {
			continue
		}
		batch.Queue(`INSERT INTO source_items (connector_id, external_id, title, url, author, content, published_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (connector_id, external_id) DO NOTHING`,
			connectorID, it.ExternalID, it.Title, it.URL, it.Author, it.Content, it.PublishedAt)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	n := 0
	for i := 0; i < batch.Len(); i++ {
		tag, err := br.Exec()
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// UnconsumedItems returns items from the given sources fetched since `since` that no pipeline run
// has used yet, newest first.
func (s *Store) UnconsumedItems(ctx context.Context, connectorIDs []uuid.UUID, since time.Time, limit int) ([]*SourceItem, error) {
	if len(connectorIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+itemColumns+` FROM source_items
		WHERE connector_id = ANY($1) AND consumed_at IS NULL AND COALESCE(published_at, fetched_at) >= $2
		ORDER BY COALESCE(published_at, fetched_at) DESC LIMIT $3`, connectorIDs, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SourceItem
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) MarkItemsConsumed(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, "UPDATE source_items SET consumed_at=NOW() WHERE id = ANY($1)", ids)
	return err
}

func (s *Store) ListItems(ctx context.Context, connectorID uuid.UUID, limit int) ([]*SourceItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT `+itemColumns+` FROM source_items WHERE connector_id=$1
		ORDER BY fetched_at DESC, COALESCE(published_at, fetched_at) DESC LIMIT $2`, connectorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SourceItem{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// SearchItems finds collected source items by keyword (title/content), newest first. Empty
// connectorIDs searches every source.
func (s *Store) SearchItems(ctx context.Context, connectorIDs []uuid.UUID, query string, since time.Time, limit int) ([]*SourceItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `SELECT `+itemColumns+` FROM source_items
		WHERE (cardinality($1::uuid[]) = 0 OR connector_id = ANY($1))
		  AND COALESCE(published_at, fetched_at) >= $2
		  AND ($3 = '' OR title ILIKE '%' || $3 || '%' OR content ILIKE '%' || $3 || '%')
		ORDER BY COALESCE(published_at, fetched_at) DESC LIMIT $4`, connectorIDs, since, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SourceItem
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
