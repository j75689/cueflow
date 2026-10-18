// Package hub instantiates stored connectors: it loads settings, decrypts secrets, and hands them
// to the plugin factory registered in package connector.
package hub

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/store"
)

type Hub struct {
	store  *store.Store
	logger *slog.Logger
	http   *http.Client
}

func New(st *store.Store, logger *slog.Logger) *Hub {
	return &Hub{store: st, logger: logger, http: &http.Client{Timeout: 90 * time.Second}}
}

// Instance builds a connector by ID. Inactive connectors are refused unless allowInactive is set
// (used by "Test" so a connector can be checked before it is enabled).
func (h *Hub) Instance(ctx context.Context, id uuid.UUID, allowInactive bool) (connector.Instance, *store.Connector, error) {
	c, cfg, err := h.store.ConnectorSettings(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if !c.IsActive && !allowInactive {
		return nil, c, fmt.Errorf("connector %q is disabled", c.Name)
	}
	inst, err := connector.Build(c.Type, cfg, connector.Env{
		ID:         c.ID,
		Name:       c.Name,
		Logger:     h.logger,
		HTTPClient: h.http,
		State:      h.store.ConnectorState(c.ID),
	})
	if err != nil {
		return nil, c, err
	}
	return inst, c, nil
}

// Close releases instance resources when the plugin holds any.
func Close(inst connector.Instance) {
	if c, ok := inst.(interface{ Close() error }); ok {
		_ = c.Close()
	}
}

func (h *Hub) Publisher(ctx context.Context, id uuid.UUID) (connector.Publisher, *store.Connector, error) {
	inst, c, err := h.Instance(ctx, id, false)
	if err != nil {
		return nil, c, err
	}
	p, ok := inst.(connector.Publisher)
	if !ok {
		return nil, c, fmt.Errorf("connector %q is not a publisher", c.Name)
	}
	return p, c, nil
}

func (h *Hub) Actor(ctx context.Context, id uuid.UUID) (connector.Actor, *store.Connector, error) {
	inst, c, err := h.Instance(ctx, id, false)
	if err != nil {
		return nil, c, err
	}
	a, ok := inst.(connector.Actor)
	if !ok {
		return nil, c, fmt.Errorf("connector %q is not an action connector", c.Name)
	}
	return a, c, nil
}

func (h *Hub) Source(ctx context.Context, id uuid.UUID) (connector.Source, *store.Connector, error) {
	inst, c, err := h.Instance(ctx, id, false)
	if err != nil {
		return nil, c, err
	}
	s, ok := inst.(connector.Source)
	if !ok {
		return nil, c, fmt.Errorf("connector %q is not a source", c.Name)
	}
	return s, c, nil
}

func (h *Hub) Searcher(ctx context.Context, id uuid.UUID) (connector.Searcher, *store.Connector, error) {
	inst, c, err := h.Instance(ctx, id, false)
	if err != nil {
		return nil, c, err
	}
	s, ok := inst.(connector.Searcher)
	if !ok {
		return nil, c, fmt.Errorf("connector %q is not a search engine", c.Name)
	}
	return s, c, nil
}

func (h *Hub) LLM(ctx context.Context, id uuid.UUID) (connector.LLM, *store.Connector, error) {
	inst, c, err := h.Instance(ctx, id, false)
	if err != nil {
		return nil, c, err
	}
	l, ok := inst.(connector.LLM)
	if !ok {
		return nil, c, fmt.Errorf("connector %q is not an LLM", c.Name)
	}
	return l, c, nil
}

func (h *Hub) ImageGenerator(ctx context.Context, id uuid.UUID) (connector.ImageGenerator, *store.Connector, error) {
	inst, c, err := h.Instance(ctx, id, false)
	if err != nil {
		return nil, c, err
	}
	g, ok := inst.(connector.ImageGenerator)
	if !ok {
		return nil, c, fmt.Errorf("connector %q does not generate images", c.Name)
	}
	return g, c, nil
}

func (h *Hub) Notifier(ctx context.Context, id uuid.UUID) (connector.Notifier, *store.Connector, error) {
	inst, c, err := h.Instance(ctx, id, false)
	if err != nil {
		return nil, c, err
	}
	n, ok := inst.(connector.Notifier)
	if !ok {
		return nil, c, fmt.Errorf("connector %q is not a notifier", c.Name)
	}
	return n, c, nil
}
