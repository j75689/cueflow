package api

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/store"
)

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.TaskFilter{Query: q.Get("q"), All: q.Get("all") == "1" || q.Get("all") == "true",
		CreatedBy: q.Get("created_by"), Limit: queryInt(r, "limit", 500)}
	for _, raw := range q["status"] {
		for _, st := range strings.Split(raw, ",") {
			ts := store.TaskStatus(strings.ToUpper(strings.TrimSpace(st)))
			if !ts.Valid() {
				writeError(w, http.StatusBadRequest, "invalid_argument", "unknown status "+st)
				return
			}
			f.Statuses = append(f.Statuses, ts)
		}
	}
	switch a := q.Get("assignee"); a {
	case "":
	case "none":
		f.Unassigned = true
	default:
		id, err := uuid.Parse(a)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", "assignee must be an agent id or 'none'")
			return
		}
		f.AssigneeID = &id
	}
	var err error
	if f.ParentID, err = queryUUID(r, "parent_id"); err != nil {
		s.fail(w, err)
		return
	}
	tasks, err := s.Store.ListTasks(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": tasks})
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	t, err := s.Store.GetTask(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	subs, err := s.Store.ListTasks(ctx, store.TaskFilter{ParentID: &id, All: true})
	if err != nil {
		s.fail(w, err)
		return
	}
	posts, err := s.Store.PostsForTask(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	var parent *store.Task
	if t.ParentID != nil {
		parent, _ = s.Store.GetTask(ctx, *t.ParentID)
	}
	writeJSON(w, http.StatusOK, struct {
		*store.Task
		Subtasks []*store.Task   `json:"subtasks"`
		Parent   *store.Task     `json:"parent"`
		Posts    []store.PostRef `json:"posts"`
	}{t, subs, parent, posts})
}

type createTaskReq struct {
	Title           string           `json:"title"`
	Description     string           `json:"description"`
	AssigneeAgentID *uuid.UUID       `json:"assignee_agent_id"`
	Priority        string           `json:"priority"`
	Labels          []string         `json:"labels"`
	DueAt           *time.Time       `json:"due_at"`
	Status          store.TaskStatus `json:"status"`
	ParentID        *uuid.UUID       `json:"parent_id"`
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskReq
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	t, err := s.Store.CreateTask(r.Context(), store.NewTask{
		Title: req.Title, Description: req.Description, AssigneeAgentID: req.AssigneeAgentID, Priority: req.Priority,
		Labels: req.Labels, DueAt: req.DueAt, Status: req.Status, ParentID: req.ParentID, CreatedBy: "human",
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Broker.Publish("task.updated", map[string]any{"id": t.ID, "status": t.Status})
	s.Crew.TaskChanged(r.Context(), t)
	writeJSON(w, http.StatusCreated, t)
}

type updateTaskReq struct {
	Title           *string             `json:"title"`
	Description     *string             `json:"description"`
	Status          *store.TaskStatus   `json:"status"`
	Priority        *string             `json:"priority"`
	Labels          *[]string           `json:"labels"`
	AssigneeAgentID nullableID          `json:"assignee_agent_id"`
	DueAt           optional[time.Time] `json:"due_at"`
	Position        *float64            `json:"position"`
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req updateTaskReq
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	patch := store.TaskPatch{Title: req.Title, Description: req.Description, Status: req.Status, Priority: req.Priority,
		Labels: req.Labels, Position: req.Position}
	if req.AssigneeAgentID.Set {
		patch.AssigneeAgentID = &req.AssigneeAgentID.Value
	}
	if req.DueAt.Set {
		patch.DueAt = &req.DueAt.Value
	}
	before, after, err := s.Store.UpdateTask(r.Context(), id, patch)
	if err != nil {
		s.fail(w, err)
		return
	}
	if before.Status != after.Status || !sameAgent(before.AssigneeAgentID, after.AssigneeAgentID) {
		if before.Status == store.TaskInProgress {
			s.Crew.Cancel(id) // a human moved or reassigned a running card
		}
		if before.Status != after.Status {
			_, _ = s.Store.AddTaskMessage(r.Context(), id, "system", nil, "status",
				"Moved to "+string(after.Status)+" by a human.", nil)
		}
		s.Crew.TaskChanged(r.Context(), after)
	}
	s.Broker.Publish("task.updated", map[string]any{"id": after.ID, "status": after.Status})
	writeJSON(w, http.StatusOK, after)
}

func sameAgent(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Crew.Cancel(id)
	if err := s.Store.DeleteTask(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	s.Broker.Publish("task.deleted", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) taskMessages(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, err := s.Store.GetTask(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	msgs, err := s.Store.TaskMessages(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

var mentionRe = regexp.MustCompile(`^@([a-z0-9][a-z0-9-]*)\b`)

// postTaskMessage records a human comment. A leading @slug hands the task to that agent; if the
// task has an agent and is not parked in BACKLOG/CANCELED, the agent is (re)started to respond.
func (s *Server) postTaskMessage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		writeError(w, http.StatusBadRequest, "invalid_argument", "content is required")
		return
	}
	ctx := r.Context()
	t, err := s.Store.GetTask(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if m := mentionRe.FindStringSubmatch(strings.ToLower(content)); m != nil {
		if a, err := s.Store.GetAgentBySlug(ctx, m[1]); err == nil && !sameAgent(t.AssigneeAgentID, &a.ID) {
			aid := &a.ID
			if _, after, err := s.Store.UpdateTask(ctx, id, store.TaskPatch{AssigneeAgentID: &aid}); err == nil {
				if t.Status == store.TaskInProgress {
					s.Crew.Cancel(id)
				}
				_, _ = s.Store.AddTaskMessage(ctx, id, "system", nil, "status", "Handed over to @"+a.Slug+".", nil)
				t = after
			}
		}
	}
	msg, err := s.Store.AddTaskMessage(ctx, id, "human", nil, "message", content, nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Broker.Publish("task.message", map[string]any{"task_id": id, "id": msg.ID})

	if t.AssigneeAgentID != nil {
		switch t.Status {
		case store.TaskBacklog, store.TaskCanceled, store.TaskTodo:
			// parked, or already queued
		case store.TaskInProgress:
			// the runner notices the new comment when this run ends and goes again
		default:
			st := store.TaskTodo
			if _, after, err := s.Store.UpdateTask(ctx, id, store.TaskPatch{Status: &st}); err == nil {
				s.Broker.Publish("task.updated", map[string]any{"id": id, "status": after.Status})
				s.Crew.TaskChanged(ctx, after)
			}
		}
	}
	writeJSON(w, http.StatusCreated, msg)
}

func (s *Server) runTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	t, err := s.Store.GetTask(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if t.AssigneeAgentID == nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "assign the task to an agent first")
		return
	}
	if t.Status == store.TaskInProgress {
		writeJSON(w, http.StatusOK, t)
		return
	}
	st := store.TaskTodo
	_, after, err := s.Store.UpdateTask(r.Context(), id, store.TaskPatch{Status: &st})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Broker.Publish("task.updated", map[string]any{"id": id, "status": after.Status})
	s.Crew.TaskChanged(r.Context(), after)
	writeJSON(w, http.StatusOK, after)
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ok, err := s.Store.SetTaskStatusIf(r.Context(), id, []store.TaskStatus{store.TaskInProgress, store.TaskTodo}, store.TaskWaitingHuman, nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Crew.Cancel(id)
	t, err := s.Store.GetTask(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if ok {
		_, _ = s.Store.AddTaskMessage(r.Context(), id, "system", nil, "status", "Stopped by a human.", nil)
		s.Broker.Publish("task.updated", map[string]any{"id": id, "status": t.Status})
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) taskRuns(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	runs, err := s.Store.ListAgentRuns(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}
