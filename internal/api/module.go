package api

import (
	"context"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/store"
)

// Module is a feature set mounted on the server. The core serves orgs, tasks, agents, schedules,
// connectors, runners and media; the content workflow (posts and their review, simple schedules,
// publishing) is a module. A module may also implement the optional interfaces below.
type Module interface {
	ID() string
	// Routes registers the module's endpoints on r, which is /api/v1 behind the same auth as the core's.
	Routes(s *Server, r chi.Router)
}

// MetaProvider adds keys to GET /meta (constants the dashboard needs for the module's pages).
type MetaProvider interface {
	Meta(s *Server, out map[string]any)
}

// StatsProvider adds keys to GET /stats.
type StatsProvider interface {
	Stats(ctx context.Context, s *Server, org uuid.UUID, out map[string]any) error
}

// TaskDetailer adds what the module attached to a task to GET /tasks/{id} and the MCP get_task.
type TaskDetailer interface {
	TaskDetails(ctx context.Context, s *Server, t *store.Task, out map[string]any) error
}

// MCPProvider adds tools to the operator MCP endpoint (/mcp) and a sentence to its instructions.
type MCPProvider interface {
	MCPTools(s *Server, srv *mcp.Server)
	MCPInstructions() string
}

func (s *Server) moduleIDs() []string {
	ids := make([]string, 0, len(s.Modules))
	for _, m := range s.Modules {
		ids = append(ids, m.ID())
	}
	return ids
}

// taskDetails collects every module's additions to a task.
func (s *Server) taskDetails(ctx context.Context, t *store.Task, out map[string]any) error {
	for _, m := range s.Modules {
		if d, ok := m.(TaskDetailer); ok {
			if err := d.TaskDetails(ctx, s, t, out); err != nil {
				return err
			}
		}
	}
	return nil
}
