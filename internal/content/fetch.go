package content

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/hub"
)

// FetchSource pulls new items from a source connector into the store and returns how many were new.
func FetchSource(ctx context.Context, h *hub.Hub, st *Store, id uuid.UUID, since time.Time, limit int) (int, error) {
	src, c, err := h.Source(ctx, id)
	if err != nil {
		return 0, err
	}
	defer hub.Close(src)
	items, err := src.Fetch(ctx, connector.FetchOptions{Since: since, Limit: limit})
	if err != nil {
		_ = st.SetConnectorCheck(ctx, c.ID, false, err.Error())
		return 0, err
	}
	n, err := st.InsertItems(ctx, c.ID, items)
	if err != nil {
		return n, err
	}
	_ = st.SetConnectorCheck(ctx, c.ID, true, "")
	return n, nil
}
