package api

import (
	"context"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

// Content is the content workflow module: posts and their review, simple schedules (pipelines) and
// their runs, the items source connectors collect and the accounts publishers offer. Its handlers
// still live in this package; the module is how the server reaches them.
type Content struct{}

func (Content) ID() string { return "content" }

func (Content) Routes(s *Server, r chi.Router) {
	r.Get("/posts", s.listPosts)
	r.Post("/posts", s.createPost)
	r.Get("/posts/{id}", s.getPost)
	r.Patch("/posts/{id}", s.updatePost)
	r.Delete("/posts/{id}", s.deletePost)
	r.Post("/posts/{id}/submit", s.submitPost)
	r.Post("/posts/{id}/approve", s.approvePost)
	r.Post("/posts/{id}/reject", s.rejectPost)
	r.Post("/posts/{id}/retry", s.retryPost)
	r.Get("/posts/{id}/audit", s.postAudit)

	r.Post("/connectors/{id}/fetch", s.fetchConnector)
	r.Get("/connectors/{id}/items", s.connectorItems)
	r.Get("/connectors/{id}/accounts", s.connectorAccounts)

	r.Get("/pipelines", s.listPipelines)
	r.Post("/pipelines", s.createPipeline)
	r.Get("/pipelines/{id}", s.getPipeline)
	r.Patch("/pipelines/{id}", s.updatePipeline)
	r.Delete("/pipelines/{id}", s.deletePipeline)
	r.Post("/pipelines/{id}/run", s.runPipeline)
	r.Get("/runs", s.listRuns)
	r.Get("/runs/{id}", s.getRun)
}

// Meta publishes the post state machine and the platform rules the review pages need.
func (Content) Meta(_ *Server, out map[string]any) {
	transitions := map[string][]domain.Status{}
	for k, v := range domain.Transitions() {
		transitions[string(k)] = v
	}
	out["statuses"] = domain.AllStatuses
	out["transitions"] = transitions
	out["platforms"] = domain.Platforms()
}

func (Content) Stats(ctx context.Context, s *Server, org uuid.UUID, out map[string]any) error {
	byStatus, err := s.Store.CountPostsByStatus(ctx, org)
	if err != nil {
		return err
	}
	_, _, pt, pa, err := s.Store.Counts(ctx, org)
	if err != nil {
		return err
	}
	var last *store.Run
	if runs, err := s.Store.ListRuns(ctx, &org, nil, 1); err == nil && len(runs) > 0 {
		last = runs[0]
	}
	out["posts_by_status"] = byStatus
	out["pipelines"] = map[string]int{"total": pt, "active": pa}
	out["last_run"] = last
	return nil
}

// TaskDetails lists the posts drafted for a task.
func (Content) TaskDetails(ctx context.Context, s *Server, t *store.Task, out map[string]any) error {
	posts, err := s.Store.PostsForTask(ctx, t.ID)
	if err != nil {
		return err
	}
	out["posts"] = posts
	return nil
}

func (Content) MCPInstructions() string {
	return "Anything agents draft for publishing waits for a human to review it in the CueFlow dashboard; nothing here publishes."
}

type mcpListDraftsIn struct {
	Status string `json:"status,omitempty" jsonschema:"post status (default PENDING_REVIEW): DRAFT, PENDING_REVIEW, APPROVED, REJECTED, QUEUED, PUBLISHED or FAILED"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum posts to return (default 20)"`
	Org    string `json:"org,omitempty" jsonschema:"org slug (see list_orgs); blank means the default org"`
}

func (Content) MCPTools(s *Server, srv *mcp.Server) {
	mcp.AddTool(srv, mcpTool("list_drafts", "List social posts by status (default: drafts waiting for human review). Read-only: approving and publishing happen in the CueFlow dashboard.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpListDraftsIn) (*mcp.CallToolResult, any, error) {
			st := domain.Status(strings.ToUpper(strings.TrimSpace(in.Status)))
			if st == "" {
				st = domain.StatusPendingReview
			}
			if !slices.Contains(domain.AllStatuses, st) {
				return mcpError("unknown post status %q", in.Status)
			}
			limit := in.Limit
			if limit <= 0 || limit > 100 {
				limit = 20
			}
			org, err := s.mcpOrg(ctx, in.Org)
			if err != nil {
				return mcpErr(err)
			}
			posts, total, err := s.Store.ListPosts(ctx, store.PostFilter{OrgID: &org.ID, Statuses: []domain.Status{st}, Limit: limit})
			if err != nil {
				return nil, nil, err
			}
			out := make([]map[string]any, 0, len(posts))
			for _, p := range posts {
				out = append(out, map[string]any{"id": p.ID, "status": p.Status, "platforms": p.Platforms,
					"content": p.Content, "task_id": p.TaskID, "updated_at": p.UpdatedAt})
			}
			return mcpJSON(map[string]any{"total": total, "items": out})
		})
}
