package content

import (
	"log/slog"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/j75689/cueflow/internal/api"
	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/store"
)

// registerOnce wires the module into the core's registries the first time a Module is attached:
// the platform validator for agents and the agents' capabilities and tools.
var registerOnce sync.Once

func register() {
	registerOnce.Do(func() {
		store.ValidatePlatforms = ValidatePlatforms
		registerTools()
		registerPostTools()
		crew.RegisterTeam(socialContentTeam())
	})
}

// Module is the social content module on top of deliverables: the "post" deliverable kind (platform
// rules, publishing through a publisher connector), the material source connectors collect, the
// accounts publishers offer, and the agents' tools for both.
type Module struct {
	Store  *Store
	Hub    *hub.Hub
	Broker *events.Broker
	Logger *slog.Logger
	// MediaBaseURL turns stored media ("/media/{id}") into URLs publishers can fetch.
	MediaBaseURL string
}

func (m *Module) ID() string { return "content" }

// Attach wires the module into the core: registries, and the post kind with this module's
// publishing action. Call it before the server and the crew start.
func (m *Module) Attach() {
	register()
	deliverable.RegisterKind(m.postKind())
}

func (m *Module) Routes(s *api.Server, r chi.Router) {
	h := &handlers{m: m, s: s}
	r.Post("/connectors/{id}/fetch", h.fetchConnector)
	r.Get("/connectors/{id}/items", h.connectorItems)
	r.Get("/connectors/{id}/accounts", h.connectorAccounts)
}

// Meta publishes the platform rules the post editor needs.
func (m *Module) Meta(_ *api.Server, out map[string]any) {
	out["platforms"] = Platforms()
}

func (m *Module) MCPInstructions() string {
	return "Social posts are deliverables of kind post: they wait for a human to approve them in the CueFlow dashboard, and only then are published."
}

// handlers serves the module's REST endpoints; h.s is the core server (auth, orgs, error mapping).
type handlers struct {
	m *Module
	s *api.Server
}
