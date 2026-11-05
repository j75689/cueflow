package sqlite

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/content"
	storesqlite "github.com/j75689/cueflow/internal/store/sqlite"
)

const itemColumns = `id, connector_id, external_id, title, url, author, content, published_at, fetched_at, consumed_at`

func scanItem(row storesqlite.Row) (*content.SourceItem, error) {
	var it content.SourceItem
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
	n := 0
	err := s.Tx(ctx, func(tx storesqlite.Querier) error {
		for _, it := range items {
			if it.ExternalID == "" {
				continue
			}
			tag, err := tx.Exec(ctx, `INSERT INTO source_items (connector_id, external_id, title, url, author, content, published_at, id, fetched_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$now) ON CONFLICT (connector_id, external_id) DO NOTHING`,
				connectorID, it.ExternalID, it.Title, it.URL, it.Author, it.Content, it.PublishedAt, uuid.New())
			if err != nil {
				return err
			}
			n += int(tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// UnconsumedItems returns items from the given sources fetched since `since` that no pipeline run
// has used yet, newest first.
func (s *Store) UnconsumedItems(ctx context.Context, connectorIDs []uuid.UUID, since time.Time, limit int) ([]*content.SourceItem, error) {
	if len(connectorIDs) == 0 {
		return nil, nil
	}
	rows, err := s.Reader().Query(ctx, `SELECT `+itemColumns+` FROM source_items
		WHERE connector_id IN (SELECT value FROM json_each($1)) AND consumed_at IS NULL AND COALESCE(published_at, fetched_at) >= $2
		ORDER BY COALESCE(published_at, fetched_at) DESC LIMIT $3`, connectorIDs, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*content.SourceItem
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
	_, err := s.Writer().Exec(ctx, "UPDATE source_items SET consumed_at=$now WHERE id IN (SELECT value FROM json_each($1))", ids)
	return err
}

func (s *Store) ListItems(ctx context.Context, connectorID uuid.UUID, limit int) ([]*content.SourceItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.Reader().Query(ctx, `SELECT `+itemColumns+` FROM source_items WHERE connector_id=$1
		ORDER BY fetched_at DESC, COALESCE(published_at, fetched_at) DESC LIMIT $2`, connectorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*content.SourceItem{}
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
func (s *Store) SearchItems(ctx context.Context, connectorIDs []uuid.UUID, query string, since time.Time, limit int) ([]*content.SourceItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := s.Reader().Query(ctx, `SELECT `+itemColumns+` FROM source_items
		WHERE (json_array_length($1) = 0 OR connector_id IN (SELECT value FROM json_each($1)))
		  AND COALESCE(published_at, fetched_at) >= $2
		  AND ($3 = '' OR title LIKE '%' || $3 || '%' OR content LIKE '%' || $3 || '%')
		ORDER BY COALESCE(published_at, fetched_at) DESC LIMIT $4`, nonNil(connectorIDs), since, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*content.SourceItem
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func nonNil(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}
