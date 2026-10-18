package content

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
	"github.com/j75689/cueflow/internal/scheduler"
	"github.com/j75689/cueflow/internal/store"
)

// Job kinds the module registers with package jobs (see Module.Attach).
const (
	// KindDispatch publishes queued posts (id is a post id, or uuid.Nil for "whichever is next").
	KindDispatch jobs.Kind = "dispatch"
	// KindPipeline runs one simple schedule (pipeline) to completion; id is the pipeline id.
	KindPipeline jobs.Kind = "pipeline"
)

// registerOnce wires the module into the core's registries the first time a Module is attached:
// job kinds, the platform validator and clock table for the store, the agents' capabilities and
// tools. A binary that does not mount the module never runs it.
var registerOnce sync.Once

func register() {
	registerOnce.Do(func() {
		jobs.Register(KindDispatch)
		jobs.Register(KindPipeline)
		store.ValidatePlatforms = ValidatePlatforms
		store.ClockTables = append(store.ClockTables, "pipelines")
		registerTools()
	})
}

// PipelineRunner runs simple schedules (package pipeline implements it).
type PipelineRunner interface {
	Trigger(ctx context.Context, pipelineID uuid.UUID, trigger string) (uuid.UUID, error)
	RunSync(ctx context.Context, pipelineID uuid.UUID, trigger string) error
}

// Dispatcher is the part of the publish worker the module needs (package dispatch implements it).
type Dispatcher interface{ Wake() }

// Module is the content workflow mounted on the core: posts and their review, simple schedules
// (pipelines) and their runs, the items source connectors collect, the accounts publishers offer,
// the agents' drafting tools and the review digest.
type Module struct {
	Store      *Store
	Hub        *hub.Hub
	Broker     *events.Broker
	Scheduler  *scheduler.Scheduler
	Runner     PipelineRunner
	Dispatcher Dispatcher
	Logger     *slog.Logger
	// PublicURL is how people reach the dashboard (links in the review digest).
	PublicURL string
}

func (m *Module) ID() string { return "content" }

// Attach wires the module into the core (register) and its clockwork into the scheduler:
// pipelines fire on their cron expressions and, when digestCron is set, the review digest goes
// out on it. Call it before the scheduler starts.
func (m *Module) Attach(digestCron string) {
	register()
	m.Scheduler.AddSource(&pipelineSource{m: m})
	if digestCron != "" {
		m.Scheduler.AddPeriodic("digest", digestCron, m.sendDigest)
	}
}

func (m *Module) Routes(s *api.Server, r chi.Router) {
	h := &handlers{m: m, s: s}
	r.Get("/posts", h.listPosts)
	r.Post("/posts", h.createPost)
	r.Get("/posts/{id}", h.getPost)
	r.Patch("/posts/{id}", h.updatePost)
	r.Delete("/posts/{id}", h.deletePost)
	r.Post("/posts/{id}/submit", h.submitPost)
	r.Post("/posts/{id}/approve", h.approvePost)
	r.Post("/posts/{id}/reject", h.rejectPost)
	r.Post("/posts/{id}/retry", h.retryPost)
	r.Get("/posts/{id}/audit", h.postAudit)

	r.Post("/connectors/{id}/fetch", h.fetchConnector)
	r.Get("/connectors/{id}/items", h.connectorItems)
	r.Get("/connectors/{id}/accounts", h.connectorAccounts)

	r.Get("/pipelines", h.listPipelines)
	r.Post("/pipelines", h.createPipeline)
	r.Get("/pipelines/{id}", h.getPipeline)
	r.Patch("/pipelines/{id}", h.updatePipeline)
	r.Delete("/pipelines/{id}", h.deletePipeline)
	r.Post("/pipelines/{id}/run", h.runPipeline)
	r.Get("/runs", h.listRuns)
	r.Get("/runs/{id}", h.getRun)
}

// Meta publishes the post state machine and the platform rules the review pages need.
func (m *Module) Meta(_ *api.Server, out map[string]any) {
	transitions := map[string][]Status{}
	for k, v := range Transitions() {
		transitions[string(k)] = v
	}
	out["statuses"] = AllStatuses
	out["transitions"] = transitions
	out["platforms"] = Platforms()
}

func (m *Module) Stats(ctx context.Context, _ *api.Server, org uuid.UUID, out map[string]any) error {
	byStatus, err := m.Store.CountPostsByStatus(ctx, org)
	if err != nil {
		return err
	}
	pt, pa, err := m.Store.CountPipelines(ctx, org)
	if err != nil {
		return err
	}
	var last *Run
	if runs, err := m.Store.ListRuns(ctx, &org, nil, 1); err == nil && len(runs) > 0 {
		last = runs[0]
	}
	out["posts_by_status"] = byStatus
	out["pipelines"] = map[string]int{"total": pt, "active": pa}
	out["last_run"] = last
	return nil
}

// TaskDetails lists the posts drafted for a task.
func (m *Module) TaskDetails(ctx context.Context, _ *api.Server, t *store.Task, out map[string]any) error {
	posts, err := m.Store.PostsForTask(ctx, t.ID)
	if err != nil {
		return err
	}
	out["posts"] = posts
	return nil
}

func (m *Module) MCPInstructions() string {
	return "Anything agents draft for publishing waits for a human to review it in the CueFlow dashboard; nothing here publishes."
}

type mcpListDraftsIn struct {
	Status string `json:"status,omitempty" jsonschema:"post status (default PENDING_REVIEW): DRAFT, PENDING_REVIEW, APPROVED, REJECTED, QUEUED, PUBLISHED or FAILED"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum posts to return (default 20)"`
	Org    string `json:"org,omitempty" jsonschema:"org slug (see list_orgs); blank means the default org"`
}

func (m *Module) MCPTools(s *api.Server, srv *mcp.Server) {
	mcp.AddTool(srv, api.MCPTool("list_drafts", "List social posts by status (default: drafts waiting for human review). Read-only: approving and publishing happen in the CueFlow dashboard.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpListDraftsIn) (*mcp.CallToolResult, any, error) {
			st := Status(strings.ToUpper(strings.TrimSpace(in.Status)))
			if st == "" {
				st = StatusPendingReview
			}
			if !slices.Contains(AllStatuses, st) {
				return api.MCPError("unknown post status %q", in.Status)
			}
			limit := in.Limit
			if limit <= 0 || limit > 100 {
				limit = 20
			}
			org, err := s.MCPOrg(ctx, in.Org)
			if err != nil {
				return api.MCPErr(err)
			}
			posts, total, err := m.Store.ListPosts(ctx, PostFilter{OrgID: &org.ID, Statuses: []Status{st}, Limit: limit})
			if err != nil {
				return nil, nil, err
			}
			out := make([]map[string]any, 0, len(posts))
			for _, p := range posts {
				out = append(out, map[string]any{"id": p.ID, "status": p.Status, "platforms": p.Platforms,
					"content": p.Content, "task_id": p.TaskID, "updated_at": p.UpdatedAt})
			}
			return api.MCPJSON(map[string]any{"total": total, "items": out})
		})
}

// pipelineSource feeds the scheduler the active pipelines.
type pipelineSource struct{ m *Module }

func (p *pipelineSource) Kind() jobs.Kind { return KindPipeline }
func (p *pipelineSource) Table() string   { return "pipelines" }

func (p *pipelineSource) Active(ctx context.Context) ([]scheduler.CronEntry, error) {
	pipelines, err := p.m.Store.ListPipelines(ctx, nil)
	if err != nil {
		return nil, err
	}
	var out []scheduler.CronEntry
	for _, pl := range pipelines {
		if pl.IsActive {
			out = append(out, scheduler.CronEntry{ID: pl.ID, Expr: pl.CronExpr, Timezone: pl.Timezone})
		}
	}
	return out, nil
}

func (p *pipelineSource) Entry(ctx context.Context, id uuid.UUID) (*scheduler.CronEntry, error) {
	pl, err := p.m.Store.GetPipeline(ctx, id)
	if err != nil || !pl.IsActive {
		return nil, err
	}
	return &scheduler.CronEntry{ID: pl.ID, Expr: pl.CronExpr, Timezone: pl.Timezone}, nil
}

func (p *pipelineSource) Fire(ctx context.Context, id uuid.UUID) error {
	return p.m.Runner.RunSync(ctx, id, "cron")
}
