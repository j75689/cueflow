package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.TaskFilter{Query: q.Get("q"), All: q.Get("all") == "1" || q.Get("all") == "true",
		CreatedBy: q.Get("created_by"), Limit: QueryInt(r, "limit", 500)}
	for _, raw := range q["status"] {
		for _, st := range strings.Split(raw, ",") {
			ts := store.TaskStatus(strings.ToUpper(strings.TrimSpace(st)))
			if !ts.Valid() {
				WriteError(w, http.StatusBadRequest, "invalid_argument", "unknown status "+st)
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
			WriteError(w, http.StatusBadRequest, "invalid_argument", "assignee must be an agent id or 'none'")
			return
		}
		f.AssigneeID = &id
	}
	org, err := s.ReqOrg(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	f.OrgID = &org.ID
	if f.ParentID, err = QueryUUID(r, "parent_id"); err != nil {
		s.Fail(w, err)
		return
	}
	tasks, err := s.Store.ListTasks(r.Context(), f)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": tasks})
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	ctx := r.Context()
	t, err := s.Store.GetTask(ctx, id)
	if err != nil {
		s.Fail(w, err)
		return
	}
	subs, err := s.Store.ListTasks(ctx, store.TaskFilter{ParentID: &id, All: true})
	if err != nil {
		s.Fail(w, err)
		return
	}
	var parent *store.Task
	if t.ParentID != nil {
		parent, _ = s.Store.GetTask(ctx, *t.ParentID)
	}
	out := asMap(t)
	out["subtasks"] = subs
	out["parent"] = parent
	if err := s.taskDetails(ctx, t, out); err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// asMap renders v's JSON form as a map, so other keys can be added next to its fields.
func asMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
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
	// Publishing defaults for drafts made for this task (a subtask inherits its parent's).
	PublisherConnectorID *uuid.UUID `json:"publisher_connector_id"`
	PublishAccounts      []string   `json:"publish_accounts"`
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskReq
	if err := Decode(r, &req); err != nil {
		s.Fail(w, err)
		return
	}
	in := store.NewTask{
		Title: req.Title, Description: req.Description, AssigneeAgentID: req.AssigneeAgentID, Priority: req.Priority,
		Labels: req.Labels, DueAt: req.DueAt, Status: req.Status, ParentID: req.ParentID,
		PublisherConnectorID: req.PublisherConnectorID, PublishAccounts: req.PublishAccounts,
	}
	if req.ParentID == nil { // a subtask lives in its parent's org
		org, err := s.ReqOrg(r)
		if err != nil {
			s.Fail(w, err)
			return
		}
		in.OrgID = org.ID
	}
	t, err := s.createTaskOp(r.Context(), in)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, t)
}

// createTaskOp creates a task on behalf of the operator (dashboard or MCP) and wakes its agent.
func (s *Server) createTaskOp(ctx context.Context, in store.NewTask) (*store.Task, error) {
	in.CreatedBy = "human"
	if in.ParentID != nil && in.PublisherConnectorID == nil {
		if parent, err := s.Store.GetTask(ctx, *in.ParentID); err == nil {
			in.PublisherConnectorID, in.PublishAccounts = parent.PublisherConnectorID, parent.PublishAccounts
		}
	}
	t, err := s.Store.CreateTask(ctx, in)
	if err != nil {
		return nil, err
	}
	s.Broker.Publish(t.OrgID, "task.updated", map[string]any{"id": t.ID, "status": t.Status})
	s.Crew.TaskChanged(ctx, t)
	return t, nil
}

type updateTaskReq struct {
	Title           *string             `json:"title"`
	Description     *string             `json:"description"`
	Status          *store.TaskStatus   `json:"status"`
	Priority        *string             `json:"priority"`
	Labels          *[]string           `json:"labels"`
	AssigneeAgentID NullableID          `json:"assignee_agent_id"`
	DueAt           Optional[time.Time] `json:"due_at"`
	Position        *float64            `json:"position"`
	// Publishing defaults for drafts made for this task.
	PublisherConnectorID NullableID `json:"publisher_connector_id"`
	PublishAccounts      *[]string  `json:"publish_accounts"`
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	var req updateTaskReq
	if err := Decode(r, &req); err != nil {
		s.Fail(w, err)
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
	if req.PublisherConnectorID.Set {
		patch.PublisherConnectorID = &req.PublisherConnectorID.Value
	}
	patch.PublishAccounts = req.PublishAccounts
	after, err := s.updateTaskOp(r.Context(), id, patch)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, after)
}

// updateTaskOp applies an operator edit; moving or reassigning a running card stops its run.
func (s *Server) updateTaskOp(ctx context.Context, id uuid.UUID, patch store.TaskPatch) (*store.Task, error) {
	before, after, err := s.Store.UpdateTask(ctx, id, patch)
	if err != nil {
		return nil, err
	}
	if before.Status != after.Status || !sameAgent(before.AssigneeAgentID, after.AssigneeAgentID) {
		if before.Status == store.TaskInProgress {
			s.Crew.Cancel(id) // a human moved or reassigned a running card
		}
		if before.Status != after.Status {
			_, _ = s.Store.AddTaskMessage(ctx, id, "system", nil, "status",
				"Moved to "+string(after.Status)+" by a human.", nil)
		}
		s.Crew.TaskChanged(ctx, after)
	}
	s.Broker.Publish(after.OrgID, "task.updated", map[string]any{"id": after.ID, "status": after.Status})
	return after, nil
}

func sameAgent(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	if err := s.deleteTaskOp(r.Context(), id); err != nil {
		s.Fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteTaskOp stops the task's run and deletes it with its subtasks; the single endpoint and bulk
// both go through it.
func (s *Server) deleteTaskOp(ctx context.Context, id uuid.UUID) error {
	s.Crew.Cancel(id)
	org := s.Store.TaskOrg(ctx, id)
	if err := s.Store.DeleteTask(ctx, id); err != nil {
		return err
	}
	s.Broker.Publish(org, "task.deleted", map[string]any{"id": id})
	return nil
}

// taskBulkLimit is how many ids one bulk request takes: the board's page size.
const taskBulkLimit = 500

// bulkTasks moves or deletes many tasks in one request. Each id goes through the single endpoint's
// op (run cancellation, system message, agent wake-up, event), so one that fails does so alone, with
// the single endpoint's code, and the rest go through; ids outside the request's org are not found.
// A database error stops the batch, and the answer is still the per-id result: what was applied
// before it, the id it hit, and the ids not tried.
func (s *Server) bulkTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string           `json:"action"`
		IDs    []string         `json:"ids"`
		Status store.TaskStatus `json:"status"`
	}
	if err := Decode(r, &req); err != nil {
		s.Fail(w, err)
		return
	}
	switch req.Action {
	case "move":
		if !req.Status.Valid() {
			s.Fail(w, store.Invalid("move needs a valid status"))
			return
		}
	case "delete":
	default:
		s.Fail(w, store.Invalid(`action must be "move" or "delete"`))
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > taskBulkLimit {
		s.Fail(w, store.Invalid(fmt.Sprintf("ids must name 1 to %d tasks", taskBulkLimit)))
		return
	}
	org, err := s.ReqOrg(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	ctx := r.Context()
	type failure struct {
		ID      string `json:"id"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	apply := func(id uuid.UUID) error {
		if s.Store.TaskOrg(ctx, id) != org.ID { // also uuid.Nil for a task that does not exist
			return domain.ErrNotFound
		}
		if req.Action == "delete" {
			return s.deleteTaskOp(ctx, id)
		}
		_, err := s.updateTaskOp(ctx, id, store.TaskPatch{Status: &req.Status})
		return err
	}
	ok, failed := []uuid.UUID{}, []failure{}
	seen := map[uuid.UUID]bool{}
	var stopped error
	for _, raw := range req.IDs {
		if stopped != nil {
			failed = append(failed, failure{raw, "not_attempted", "not attempted after an earlier error"})
			continue
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			err = domain.ErrNotFound
		} else if seen[id] {
			continue
		} else {
			seen[id] = true
			if err = apply(id); err == nil {
				ok = append(ok, id)
				continue
			}
		}
		status, code, msg := ErrorStatus(err)
		if status >= http.StatusInternalServerError || code == "canceled" {
			s.Logger.Error("bulk task op failed", "id", raw, "err", err)
			stopped = err
		}
		failed = append(failed, failure{raw, code, msg})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"ok": ok, "failed": failed})
}

func (s *Server) taskMessages(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	if _, err := s.Store.GetTask(r.Context(), id); err != nil {
		s.Fail(w, err)
		return
	}
	msgs, err := s.Store.TaskMessages(r.Context(), id)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, msgs)
}

var mentionRe = regexp.MustCompile(`^@([a-z0-9][a-z0-9-]*)\b`)

// postTaskMessage records a human comment. A leading @slug hands the task to that agent; if the
// task has an agent and is not parked in BACKLOG/CANCELED, the agent is (re)started to respond.
func (s *Server) postTaskMessage(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := Decode(r, &req); err != nil {
		s.Fail(w, err)
		return
	}
	msg, err := s.postTaskMessageOp(r.Context(), id, req.Content)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, msg)
}

func (s *Server) postTaskMessageOp(ctx context.Context, id uuid.UUID, content string) (*store.TaskMessage, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, &store.ValidationError{Msg: "content is required"}
	}
	t, err := s.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if m := mentionRe.FindStringSubmatch(strings.ToLower(content)); m != nil {
		if a, err := s.Store.GetAgentBySlug(ctx, t.OrgID, m[1]); err == nil && !sameAgent(t.AssigneeAgentID, &a.ID) {
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
		return nil, err
	}
	s.Broker.Publish(t.OrgID, "task.message", map[string]any{"task_id": id, "id": msg.ID})

	if t.AssigneeAgentID != nil {
		switch t.Status {
		case store.TaskBacklog, store.TaskCanceled, store.TaskTodo:
			// parked, or already queued
		case store.TaskInProgress:
			// the runner notices the new comment when this run ends and goes again
		default:
			st := store.TaskTodo
			if _, after, err := s.Store.UpdateTask(ctx, id, store.TaskPatch{Status: &st}); err == nil {
				s.Broker.Publish(after.OrgID, "task.updated", map[string]any{"id": id, "status": after.Status})
				s.Crew.TaskChanged(ctx, after)
			}
		}
	}
	return msg, nil
}

func (s *Server) runTask(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	t, err := s.runTaskOp(r.Context(), id)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, t)
}

func (s *Server) runTaskOp(ctx context.Context, id uuid.UUID) (*store.Task, error) {
	t, err := s.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.AssigneeAgentID == nil {
		return nil, &store.ValidationError{Msg: "assign the task to an agent first"}
	}
	if t.Status == store.TaskInProgress {
		return t, nil
	}
	st := store.TaskTodo
	_, after, err := s.Store.UpdateTask(ctx, id, store.TaskPatch{Status: &st})
	if err != nil {
		return nil, err
	}
	s.Broker.Publish(after.OrgID, "task.updated", map[string]any{"id": id, "status": after.Status})
	s.Crew.TaskChanged(ctx, after)
	return after, nil
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	t, err := s.cancelTaskOp(r.Context(), id)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, t)
}

func (s *Server) cancelTaskOp(ctx context.Context, id uuid.UUID) (*store.Task, error) {
	ok, err := s.Store.SetTaskStatusIf(ctx, id, []store.TaskStatus{store.TaskInProgress, store.TaskTodo}, store.TaskWaitingHuman, nil)
	if err != nil {
		return nil, err
	}
	s.Crew.Cancel(id)
	t, err := s.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if ok {
		_, _ = s.Store.AddTaskMessage(ctx, id, "system", nil, "status", "Stopped by a human.", nil)
		s.Broker.Publish(t.OrgID, "task.updated", map[string]any{"id": id, "status": t.Status})
	}
	return t, nil
}

func (s *Server) taskRuns(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	runs, err := s.Store.ListAgentRuns(r.Context(), id)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, runs)
}
