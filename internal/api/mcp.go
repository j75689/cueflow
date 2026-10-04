package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

// MCPPath is the operator MCP endpoint: external agents (Claude Code, Codex, Cursor...) manage
// the board through it with CUEFLOW_MCP_TOKEN. It acts for the operator on tasks, but has no tool
// that approves, schedules or publishes posts: drafts stay in the human review queue.
const MCPPath = "/mcp"

// mcpMovableStatuses are the statuses an MCP client may set, matching the board's drop targets.
// IN_PROGRESS, WAITING_HUMAN and BLOCKED are owned by the runner.
var mcpMovableStatuses = []store.TaskStatus{store.TaskBacklog, store.TaskTodo, store.TaskReview, store.TaskDone, store.TaskCanceled}

const mcpThreadLimit = 30

func (s *Server) mcpHandler() http.Handler {
	srv := s.newMCPServer()
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.MCPToken == "" {
			writeError(w, http.StatusNotFound, "mcp_disabled", "set CUEFLOW_MCP_TOKEN to enable the MCP endpoint")
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.MCPToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid MCP token")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ---- tool inputs ----

const taskRefDesc = "task number (e.g. 12 or #12) or task id"

type mcpTaskRef struct {
	Task string `json:"task" jsonschema:"task number (e.g. 12 or #12) or task id"`
}

type mcpListTasksIn struct {
	Status   []string `json:"status,omitempty" jsonschema:"filter by status: BACKLOG, TODO, IN_PROGRESS, WAITING_HUMAN, BLOCKED, REVIEW, DONE, CANCELED"`
	Assignee string   `json:"assignee,omitempty" jsonschema:"agent slug, or 'none' for unassigned"`
	Query    string   `json:"query,omitempty" jsonschema:"text to search in titles"`
	All      bool     `json:"all,omitempty" jsonschema:"include subtasks and tasks finished more than two weeks ago"`
	Limit    int      `json:"limit,omitempty" jsonschema:"maximum tasks to return (default 50)"`
}

type mcpCreateTaskIn struct {
	Title       string   `json:"title" jsonschema:"short task title"`
	Description string   `json:"description,omitempty" jsonschema:"what the agent should do, with any context it needs"`
	Assignee    string   `json:"assignee,omitempty" jsonschema:"agent slug (see list_agents); blank leaves it unassigned"`
	Status      string   `json:"status,omitempty" jsonschema:"TODO (default; an assigned agent starts right away) or BACKLOG"`
	Priority    string   `json:"priority,omitempty" jsonschema:"low, normal (default), high or urgent"`
	Labels      []string `json:"labels,omitempty"`
}

type mcpUpdateTaskIn struct {
	Task        string    `json:"task" jsonschema:"task number (e.g. 12 or #12) or task id"`
	Title       *string   `json:"title,omitempty"`
	Description *string   `json:"description,omitempty"`
	Assignee    *string   `json:"assignee,omitempty" jsonschema:"agent slug, or 'none' to unassign"`
	Status      *string   `json:"status,omitempty" jsonschema:"BACKLOG, TODO, REVIEW, DONE or CANCELED (moving a running task stops its run)"`
	Priority    *string   `json:"priority,omitempty" jsonschema:"low, normal, high or urgent"`
	Labels      *[]string `json:"labels,omitempty"`
}

type mcpCommentIn struct {
	Task    string `json:"task" jsonschema:"task number (e.g. 12 or #12) or task id"`
	Content string `json:"content" jsonschema:"message for the agent; start with @slug to hand the task to that agent"`
}

type mcpListDraftsIn struct {
	Status string `json:"status,omitempty" jsonschema:"post status (default PENDING_REVIEW): DRAFT, PENDING_REVIEW, APPROVED, REJECTED, QUEUED, PUBLISHED or FAILED"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum posts to return (default 20)"`
}

type mcpNoInput struct{}

func (s *Server) newMCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "cueflow", Title: "CueFlow", Version: Version}, &mcp.ServerOptions{
		Instructions: "CueFlow is a social-content studio run by a crew of AI agents on a kanban board. " +
			"Create tasks for agents, follow their progress, answer their questions and steer them with comments. " +
			"Agents write drafts that a human reviews in the CueFlow dashboard; nothing here publishes.",
	})
	tool := func(name, desc string, readOnly bool) *mcp.Tool {
		return &mcp.Tool{Name: name, Description: desc, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly}}
	}

	mcp.AddTool(srv, tool("list_agents", "List the crew: each agent's slug, name, role and description. Use the slug to assign tasks.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, any, error) {
			agents, err := s.Store.ListAgents(ctx)
			if err != nil {
				return nil, nil, err
			}
			out := make([]map[string]any, 0, len(agents))
			for _, a := range agents {
				out = append(out, map[string]any{"slug": a.Slug, "name": a.Name, "role": a.Role,
					"description": a.Description, "active": a.IsActive})
			}
			return mcpJSON(out)
		})

	mcp.AddTool(srv, tool("list_tasks", "List board tasks. By default shows top-level tasks, skipping those finished more than two weeks ago.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpListTasksIn) (*mcp.CallToolResult, any, error) {
			f := store.TaskFilter{Query: in.Query, All: in.All, Limit: in.Limit}
			if f.Limit <= 0 || f.Limit > 200 {
				f.Limit = 50
			}
			for _, st := range in.Status {
				ts := store.TaskStatus(strings.ToUpper(strings.TrimSpace(st)))
				if !ts.Valid() {
					return mcpError("unknown status %q", st)
				}
				f.Statuses = append(f.Statuses, ts)
			}
			if in.Assignee != "" {
				if strings.EqualFold(in.Assignee, "none") {
					f.Unassigned = true
				} else {
					a, err := s.mcpAgent(ctx, in.Assignee)
					if err != nil {
						return mcpErr(err)
					}
					f.AssigneeID = &a.ID
				}
			}
			tasks, err := s.Store.ListTasks(ctx, f)
			if err != nil {
				return nil, nil, err
			}
			out := make([]map[string]any, 0, len(tasks))
			for _, t := range tasks {
				out = append(out, mcpTaskSummary(t))
			}
			return mcpJSON(out)
		})

	mcp.AddTool(srv, tool("get_task", "Show a task with its description, subtasks, linked drafts and the latest messages in its thread.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpTaskRef) (*mcp.CallToolResult, any, error) {
			t, err := s.mcpResolveTask(ctx, in.Task)
			if err != nil {
				return mcpErr(err)
			}
			subs, err := s.Store.ListTasks(ctx, store.TaskFilter{ParentID: &t.ID, All: true})
			if err != nil {
				return nil, nil, err
			}
			posts, err := s.Store.PostsForTask(ctx, t.ID)
			if err != nil {
				return nil, nil, err
			}
			msgs, err := s.Store.TaskMessages(ctx, t.ID)
			if err != nil {
				return nil, nil, err
			}
			out := mcpTaskSummary(t)
			out["description"] = t.Description
			subOut := make([]map[string]any, 0, len(subs))
			for _, st := range subs {
				subOut = append(subOut, mcpTaskSummary(st))
			}
			out["subtasks"] = subOut
			out["drafts"] = posts
			if len(msgs) > mcpThreadLimit {
				out["earlier_messages_omitted"] = len(msgs) - mcpThreadLimit
				msgs = msgs[len(msgs)-mcpThreadLimit:]
			}
			thread := make([]map[string]any, 0, len(msgs))
			for _, m := range msgs {
				from := m.AuthorType
				if m.Agent != nil {
					from = "@" + m.Agent.Slug
				}
				thread = append(thread, map[string]any{"from": from, "kind": m.Kind, "content": m.Content, "at": m.CreatedAt})
			}
			out["thread"] = thread
			return mcpJSON(out)
		})

	mcp.AddTool(srv, tool("create_task", "Create a task on the board. An assigned TODO task starts right away; the agent reports back in the task thread.", false),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpCreateTaskIn) (*mcp.CallToolResult, any, error) {
			nt := store.NewTask{Title: in.Title, Description: in.Description, Priority: strings.ToLower(in.Priority), Labels: in.Labels}
			switch st := store.TaskStatus(strings.ToUpper(strings.TrimSpace(in.Status))); st {
			case "", store.TaskTodo:
				nt.Status = store.TaskTodo
			case store.TaskBacklog:
				nt.Status = st
			default:
				return mcpError("status must be TODO or BACKLOG")
			}
			if in.Assignee != "" {
				a, err := s.mcpAgent(ctx, in.Assignee)
				if err != nil {
					return mcpErr(err)
				}
				nt.AssigneeAgentID = &a.ID
			}
			t, err := s.createTaskOp(ctx, nt)
			if err != nil {
				return mcpErr(err)
			}
			return mcpJSON(mcpTaskSummary(t))
		})

	mcp.AddTool(srv, tool("update_task", "Edit a task's title, description, priority, labels, assignee or status. Only the fields you pass change.", false),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpUpdateTaskIn) (*mcp.CallToolResult, any, error) {
			t, err := s.mcpResolveTask(ctx, in.Task)
			if err != nil {
				return mcpErr(err)
			}
			patch := store.TaskPatch{Title: in.Title, Description: in.Description, Labels: in.Labels}
			if in.Priority != nil {
				p := strings.ToLower(*in.Priority)
				patch.Priority = &p
			}
			if in.Status != nil {
				st := store.TaskStatus(strings.ToUpper(strings.TrimSpace(*in.Status)))
				if !slices.Contains(mcpMovableStatuses, st) {
					return mcpError("status must be one of BACKLOG, TODO, REVIEW, DONE, CANCELED")
				}
				patch.Status = &st
			}
			if in.Assignee != nil {
				var id *uuid.UUID
				if ref := strings.TrimSpace(*in.Assignee); ref != "" && !strings.EqualFold(ref, "none") {
					a, err := s.mcpAgent(ctx, ref)
					if err != nil {
						return mcpErr(err)
					}
					id = &a.ID
				}
				patch.AssigneeAgentID = &id
			}
			after, err := s.updateTaskOp(ctx, t.ID, patch)
			if err != nil {
				return mcpErr(err)
			}
			return mcpJSON(mcpTaskSummary(after))
		})

	mcp.AddTool(srv, tool("comment_task", "Post a message in a task's thread as the operator. The assigned agent picks it up and continues; this is how you answer an agent that is waiting on you.", false),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpCommentIn) (*mcp.CallToolResult, any, error) {
			t, err := s.mcpResolveTask(ctx, in.Task)
			if err != nil {
				return mcpErr(err)
			}
			if _, err := s.postTaskMessageOp(ctx, t.ID, in.Content); err != nil {
				return mcpErr(err)
			}
			after, err := s.Store.GetTask(ctx, t.ID)
			if err != nil {
				return nil, nil, err
			}
			return mcpJSON(mcpTaskSummary(after))
		})

	mcp.AddTool(srv, tool("run_task", "Start (or restart) the assigned agent on a task.", false),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpTaskRef) (*mcp.CallToolResult, any, error) {
			t, err := s.mcpResolveTask(ctx, in.Task)
			if err != nil {
				return mcpErr(err)
			}
			after, err := s.runTaskOp(ctx, t.ID)
			if err != nil {
				return mcpErr(err)
			}
			return mcpJSON(mcpTaskSummary(after))
		})

	mcp.AddTool(srv, tool("stop_task", "Stop the agent working on a task; the task then waits for you.", false),
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpTaskRef) (*mcp.CallToolResult, any, error) {
			t, err := s.mcpResolveTask(ctx, in.Task)
			if err != nil {
				return mcpErr(err)
			}
			after, err := s.cancelTaskOp(ctx, t.ID)
			if err != nil {
				return mcpErr(err)
			}
			return mcpJSON(mcpTaskSummary(after))
		})

	mcp.AddTool(srv, tool("list_drafts", "List social posts by status (default: drafts waiting for human review). Read-only: approving and publishing happen in the CueFlow dashboard.", true),
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
			posts, total, err := s.Store.ListPosts(ctx, store.PostFilter{Statuses: []domain.Status{st}, Limit: limit})
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

	return srv
}

// mcpResolveTask accepts "12", "#12" or a task id.
func (s *Server) mcpResolveTask(ctx context.Context, ref string) (*store.Task, error) {
	ref = strings.TrimSpace(ref)
	if n, err := strconv.ParseInt(strings.TrimPrefix(ref, "#"), 10, 64); err == nil {
		return s.Store.GetTaskByNumber(ctx, n)
	}
	id, err := uuid.Parse(ref)
	if err != nil {
		return nil, &store.ValidationError{Msg: fmt.Sprintf("task %q: use a %s", ref, taskRefDesc)}
	}
	return s.Store.GetTask(ctx, id)
}

func (s *Server) mcpAgent(ctx context.Context, slug string) (*store.Agent, error) {
	slug = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(slug), "@"))
	a, err := s.Store.GetAgentBySlug(ctx, slug)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, &store.ValidationError{Msg: fmt.Sprintf("no agent with slug %q (see list_agents)", slug)}
	}
	return a, err
}

func mcpTaskSummary(t *store.Task) map[string]any {
	m := map[string]any{"number": t.Number, "id": t.ID, "title": t.Title, "status": t.Status,
		"priority": t.Priority, "running": t.Running, "updated_at": t.UpdatedAt}
	if t.Assignee != nil {
		m["assignee"] = t.Assignee.Slug
	}
	if len(t.Labels) > 0 {
		m["labels"] = t.Labels
	}
	if t.ParentID != nil {
		m["parent_id"] = t.ParentID
	}
	return m
}

func mcpJSON(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}

func mcpError(format string, args ...any) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}}}, nil, nil
}

// mcpErr reports caller mistakes as tool errors the model can read and fix; anything else is a
// server failure.
func mcpErr(err error) (*mcp.CallToolResult, any, error) {
	var ve *store.ValidationError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return mcpError("not found")
	case errors.As(err, &ve):
		return mcpError("%s", ve.Error())
	}
	return nil, nil, err
}
