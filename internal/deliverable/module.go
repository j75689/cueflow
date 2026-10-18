package deliverable

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/api"
	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/jobs"
	"github.com/j75689/cueflow/internal/store"
)

// registerOnce wires the module into the core's registries the first time a Module is attached.
var registerOnce sync.Once

func register() {
	registerOnce.Do(func() {
		jobs.Register(KindDeliver)
		registerTools()
	})
}

// Dispatcher is the part of the worker the handlers need (Worker implements it).
type Dispatcher interface{ Wake() }

// Module mounts deliverables on the core: REST, /meta constants, /stats, a task's deliverables,
// the MCP listing tool and (through tools.go) the agents' capability.
type Module struct {
	Store      *Store
	Hub        *hub.Hub
	Broker     *events.Broker
	Dispatcher Dispatcher
	Logger     *slog.Logger
}

func (m *Module) ID() string { return "deliverables" }

// Attach wires the module into the core: the job kind and the agents' capability. Call it before
// the server and the crew start.
func (m *Module) Attach() { register() }

func (m *Module) Routes(s *api.Server, r chi.Router) {
	h := &handlers{m: m, s: s}
	r.Get("/deliverables", h.list)
	r.Post("/deliverables", h.create)
	r.Get("/deliverables/{id}", h.get)
	r.Patch("/deliverables/{id}", h.update)
	r.Delete("/deliverables/{id}", h.remove)
	r.Post("/deliverables/{id}/submit", h.submit)
	r.Post("/deliverables/{id}/approve", h.approve)
	r.Post("/deliverables/{id}/reject", h.reject)
	r.Post("/deliverables/{id}/retry", h.retry)
	r.Get("/deliverables/{id}/audit", h.audit)
}

// Meta publishes the state machine and the registered kinds.
func (m *Module) Meta(_ *api.Server, out map[string]any) {
	transitions := map[string][]Status{}
	for k, v := range Transitions() {
		transitions[string(k)] = v
	}
	out["deliverable_statuses"] = AllStatuses
	out["deliverable_transitions"] = transitions
	out["deliverable_kinds"] = Kinds()
}

func (m *Module) Stats(ctx context.Context, _ *api.Server, org uuid.UUID, out map[string]any) error {
	byStatus, err := m.Store.CountByStatus(ctx, org)
	if err != nil {
		return err
	}
	out["deliverables_by_status"] = byStatus
	return nil
}

// TaskDetails lists the deliverables made for a task.
func (m *Module) TaskDetails(ctx context.Context, _ *api.Server, t *store.Task, out map[string]any) error {
	refs, err := m.Store.ForTask(ctx, t.ID)
	if err != nil {
		return err
	}
	out["deliverables"] = refs
	return nil
}

func (m *Module) MCPInstructions() string {
	return "Deliverables agents produce (documents and the like) wait for a human to approve them in the CueFlow dashboard before anything acts on them."
}

type mcpListIn struct {
	Status string `json:"status,omitempty" jsonschema:"status (default PENDING_REVIEW): DRAFT, PENDING_REVIEW, APPROVED, REJECTED, QUEUED, DONE or FAILED"`
	Kind   string `json:"kind,omitempty" jsonschema:"deliverable kind, e.g. document (blank means any)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum items to return (default 20)"`
	Org    string `json:"org,omitempty" jsonschema:"org slug (see list_orgs); blank means the default org"`
}

func (m *Module) MCPTools(s *api.Server, srv *mcp.Server) {
	mcp.AddTool(srv, api.MCPTool("list_deliverables", "List deliverables by status (default: waiting for human review). Read-only: approving happens in the CueFlow dashboard.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpListIn) (*mcp.CallToolResult, any, error) {
			st := Status(strings.ToUpper(strings.TrimSpace(in.Status)))
			if st == "" {
				st = StatusPendingReview
			}
			if !slices.Contains(AllStatuses, st) {
				return api.MCPError("unknown deliverable status %q", in.Status)
			}
			limit := in.Limit
			if limit <= 0 || limit > 100 {
				limit = 20
			}
			org, err := s.MCPOrg(ctx, in.Org)
			if err != nil {
				return api.MCPErr(err)
			}
			items, total, err := m.Store.List(ctx, Filter{OrgID: &org.ID, Statuses: []Status{st}, Kind: strings.TrimSpace(in.Kind), Limit: limit})
			if err != nil {
				return nil, nil, err
			}
			out := make([]map[string]any, 0, len(items))
			for _, d := range items {
				out = append(out, map[string]any{"id": d.ID, "kind": d.Kind, "title": d.Title, "status": d.Status,
					"payload": d.Payload, "task_id": d.TaskID, "updated_at": d.UpdatedAt})
			}
			return api.MCPJSON(map[string]any{"total": total, "items": out})
		})
}
