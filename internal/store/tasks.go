package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/j75689/cueflow/internal/domain"
)

type TaskStatus string

const (
	TaskBacklog      TaskStatus = "BACKLOG"
	TaskTodo         TaskStatus = "TODO"
	TaskInProgress   TaskStatus = "IN_PROGRESS"
	TaskBlocked      TaskStatus = "BLOCKED"
	TaskWaitingHuman TaskStatus = "WAITING_HUMAN"
	TaskReview       TaskStatus = "REVIEW"
	TaskDone         TaskStatus = "DONE"
	TaskCanceled     TaskStatus = "CANCELED"
)

var TaskStatuses = []TaskStatus{TaskBacklog, TaskTodo, TaskInProgress, TaskBlocked, TaskWaitingHuman, TaskReview, TaskDone, TaskCanceled}
var TaskPriorities = []string{"low", "normal", "high", "urgent"}

func (s TaskStatus) Valid() bool {
	for _, v := range TaskStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// Closed reports whether the task is finished from the board's point of view.
func (s TaskStatus) Closed() bool { return s == TaskDone || s == TaskCanceled || s == TaskReview }

func validPriority(p string) bool {
	for _, v := range TaskPriorities {
		if v == p {
			return true
		}
	}
	return false
}

type AgentRef struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Slug   string    `json:"slug"`
	Avatar string    `json:"avatar"`
}

type SubtaskCounts struct {
	Total int `json:"total"`
	Done  int `json:"done"`
}

type Task struct {
	ID              uuid.UUID  `json:"id"`
	OrgID           uuid.UUID  `json:"org_id"`
	Number          int64      `json:"number"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Status          TaskStatus `json:"status"`
	Priority        string     `json:"priority"`
	Labels          []string   `json:"labels"`
	AssigneeAgentID *uuid.UUID `json:"assignee_agent_id"`
	Assignee        *AgentRef  `json:"assignee"`
	CreatedBy       string     `json:"created_by"`
	CreatorAgentID  *uuid.UUID `json:"creator_agent_id"`
	ParentID        *uuid.UUID `json:"parent_id"`
	ScheduleID      *uuid.UUID `json:"schedule_id"`
	// PublisherConnectorID and PublishAccounts are applied to drafts agents write for this task.
	PublisherConnectorID *uuid.UUID    `json:"publisher_connector_id"`
	PublishAccounts      []string      `json:"publish_accounts"`
	SubtaskCounts        SubtaskCounts `json:"subtask_counts"`
	Result               string        `json:"result"`
	DueAt                *time.Time    `json:"due_at"`
	Position             float64       `json:"position"`
	Running              bool          `json:"running"`
	Attempts             int           `json:"attempts"`
	Version              int           `json:"version"`
	CreatedAt            time.Time     `json:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at"`
	StartedAt            *time.Time    `json:"started_at"`
	CompletedAt          *time.Time    `json:"completed_at"`
}

const taskColumns = `t.id, t.number, t.title, t.description, t.status, t.priority, t.labels, t.assignee_agent_id,
	a.id, a.name, a.slug, a.avatar, t.created_by, t.creator_agent_id, t.parent_id, t.schedule_id,
	(SELECT count(*) FROM tasks c WHERE c.parent_id=t.id),
	(SELECT count(*) FROM tasks c WHERE c.parent_id=t.id AND c.status IN ('DONE','CANCELED','REVIEW')),
	t.result, t.due_at, t.position, (t.status='IN_PROGRESS' AND t.lease_until > NOW()), t.attempts, t.version,
	t.created_at, t.updated_at, t.started_at, t.completed_at, t.publisher_connector_id, t.publish_accounts, t.org_id`

const taskFrom = ` FROM tasks t LEFT JOIN agents a ON a.id = t.assignee_agent_id`

func scanTask(row pgx.Row) (*Task, error) {
	var t Task
	var status string
	var aID *uuid.UUID
	var aName, aSlug, aAvatar *string
	if err := row.Scan(&t.ID, &t.Number, &t.Title, &t.Description, &status, &t.Priority, &t.Labels, &t.AssigneeAgentID,
		&aID, &aName, &aSlug, &aAvatar, &t.CreatedBy, &t.CreatorAgentID, &t.ParentID, &t.ScheduleID,
		&t.SubtaskCounts.Total, &t.SubtaskCounts.Done, &t.Result, &t.DueAt, &t.Position, &t.Running, &t.Attempts,
		&t.Version, &t.CreatedAt, &t.UpdatedAt, &t.StartedAt, &t.CompletedAt, &t.PublisherConnectorID, &t.PublishAccounts, &t.OrgID); err != nil {
		return nil, err
	}
	t.Status = TaskStatus(status)
	if t.PublishAccounts == nil {
		t.PublishAccounts = []string{}
	}
	if t.Labels == nil {
		t.Labels = []string{}
	}
	if aID != nil {
		t.Assignee = &AgentRef{ID: *aID, Name: deref(aName), Slug: deref(aSlug), Avatar: deref(aAvatar)}
	}
	return &t, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (s *Store) GetTask(ctx context.Context, id uuid.UUID) (*Task, error) {
	t, err := scanTask(s.pool.QueryRow(ctx, "SELECT "+taskColumns+taskFrom+" WHERE t.id=$1", id))
	return t, NotFound(err)
}

// TaskOrg is the org a task belongs to, or uuid.Nil if it is gone (live-update events use it to reach the
// right org's tabs).
func (s *Store) TaskOrg(ctx context.Context, id uuid.UUID) uuid.UUID {
	var org uuid.UUID
	_ = s.pool.QueryRow(ctx, "SELECT org_id FROM tasks WHERE id=$1", id).Scan(&org)
	return org
}

// GetTaskByNumber finds a task by its #number within an org.
func (s *Store) GetTaskByNumber(ctx context.Context, org uuid.UUID, n int64) (*Task, error) {
	t, err := scanTask(s.pool.QueryRow(ctx, "SELECT "+taskColumns+taskFrom+" WHERE t.org_id=$1 AND t.number=$2", org, n))
	return t, NotFound(err)
}

type TaskFilter struct {
	OrgID      *uuid.UUID
	Statuses   []TaskStatus
	AssigneeID *uuid.UUID
	Unassigned bool
	ParentID   *uuid.UUID
	Query      string
	All        bool // include old DONE/CANCELED
	CreatedBy  string
	Limit      int
}

func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]*Task, error) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.OrgID != nil {
		where = append(where, "t.org_id = "+arg(*f.OrgID))
	}
	if len(f.Statuses) > 0 {
		ss := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			ss[i] = string(st)
		}
		where = append(where, "t.status = ANY("+arg(ss)+")")
	}
	if f.AssigneeID != nil {
		where = append(where, "t.assignee_agent_id = "+arg(*f.AssigneeID))
	}
	if f.Unassigned {
		where = append(where, "t.assignee_agent_id IS NULL")
	}
	if f.ParentID != nil {
		where = append(where, "t.parent_id = "+arg(*f.ParentID))
	}
	if f.CreatedBy != "" {
		where = append(where, "t.created_by = "+arg(f.CreatedBy))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		p := arg("%" + q + "%")
		where = append(where, "(t.title ILIKE "+p+" OR t.description ILIKE "+p+")")
	}
	if !f.All {
		where = append(where, "NOT (t.status IN ('DONE','CANCELED') AND t.updated_at < NOW() - INTERVAL '14 days')")
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, "SELECT "+taskColumns+taskFrom+cond+
		" ORDER BY t.position, t.created_at LIMIT "+arg(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type NewTask struct {
	// OrgID may be left empty: it then comes from the parent task, the schedule or the assignee,
	// and falls back to the default org.
	OrgID           uuid.UUID
	Title           string
	Description     string
	Status          TaskStatus
	Priority        string
	Labels          []string
	AssigneeAgentID *uuid.UUID
	CreatedBy       string // human | agent
	CreatorAgentID  *uuid.UUID
	ParentID        *uuid.UUID
	ScheduleID      *uuid.UUID
	DueAt           *time.Time
	// Publishing defaults for drafts made for this task.
	PublisherConnectorID *uuid.UUID
	PublishAccounts      []string
}

func (s *Store) CreateTask(ctx context.Context, in NewTask) (*Task, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return nil, Invalid("title is required")
	}
	if in.Priority == "" {
		in.Priority = "normal"
	}
	if !validPriority(in.Priority) {
		return nil, Invalid("priority must be low, normal, high or urgent")
	}
	if in.Status == "" {
		in.Status = TaskBacklog
		if in.AssigneeAgentID != nil {
			in.Status = TaskTodo
		}
	}
	if !in.Status.Valid() || in.Status == TaskInProgress {
		return nil, Invalid("invalid initial status")
	}
	if in.CreatedBy == "" {
		in.CreatedBy = "human"
	}
	var id uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		org, err := s.taskOrg(ctx, tx, in)
		if err != nil {
			return err
		}
		for _, ref := range []struct {
			table, what string
			id          *uuid.UUID
		}{{"tasks", "parent task", in.ParentID}, {"schedules", "schedule", in.ScheduleID},
			{"agents", "assignee", in.AssigneeAgentID}, {"agents", "creator agent", in.CreatorAgentID}} {
			if err := sameOrg(ctx, tx, org, ref.table, ref.what, ref.id); err != nil {
				return err
			}
		}
		// The org row lock serializes numbering, so numbers never collide.
		var number int64
		if err := tx.QueryRow(ctx, `UPDATE orgs SET next_task_number = next_task_number + 1 WHERE id=$1
			RETURNING next_task_number - 1`, org).Scan(&number); err != nil {
			return NotFound(err)
		}
		if err := tx.QueryRow(ctx, `INSERT INTO tasks (title, description, status, priority, labels, assignee_agent_id,
			created_by, creator_agent_id, parent_id, schedule_id, due_at, publisher_connector_id, publish_accounts, org_id, number, position)
			VALUES ($1,$2,$3::text,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
			  COALESCE((SELECT max(position) FROM tasks WHERE status=$3::text AND org_id=$14), 0) + 1024) RETURNING id`,
			truncate(in.Title, 255), in.Description, string(in.Status), in.Priority, cleanLabels(in.Labels),
			in.AssigneeAgentID, in.CreatedBy, in.CreatorAgentID, in.ParentID, in.ScheduleID, in.DueAt,
			in.PublisherConnectorID, cleanList(in.PublishAccounts), org, number).Scan(&id); err != nil {
			return NotFound(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetTask(ctx, id)
}

// taskOrg picks the org of a new task: the given one, else the parent's, the schedule's or the
// assignee's, else the default org.
func (s *Store) taskOrg(ctx context.Context, q querier, in NewTask) (uuid.UUID, error) {
	if in.OrgID != uuid.Nil {
		return in.OrgID, nil
	}
	for _, ref := range []struct {
		table string
		id    *uuid.UUID
	}{{"tasks", in.ParentID}, {"schedules", in.ScheduleID}, {"agents", in.AssigneeAgentID}} {
		if ref.id != nil {
			if org, err := orgOf(ctx, q, ref.table, *ref.id); err == nil {
				return org, nil
			}
		}
	}
	var org uuid.UUID
	err := q.QueryRow(ctx, "SELECT id FROM orgs ORDER BY created_at, id LIMIT 1").Scan(&org)
	return org, NotFound(err)
}

type TaskPatch struct {
	Title           *string
	Description     *string
	Status          *TaskStatus
	Priority        *string
	Labels          *[]string
	AssigneeAgentID **uuid.UUID
	DueAt           **time.Time
	Position        *float64
	// Publishing defaults: both are set together.
	PublisherConnectorID **uuid.UUID
	PublishAccounts      *[]string
}

// UpdateTask applies a human edit. It returns the task before and after the change.
func (s *Store) UpdateTask(ctx context.Context, id uuid.UUID, p TaskPatch) (before, after *Task, err error) {
	before, err = s.GetTask(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	sets := []string{"updated_at=NOW()", "version=version+1"}
	args := []any{id}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s=$%d", col, len(args)))
	}
	if p.Title != nil {
		t := strings.TrimSpace(*p.Title)
		if t == "" {
			return nil, nil, Invalid("title is required")
		}
		add("title", truncate(t, 255))
	}
	if p.Description != nil {
		add("description", *p.Description)
	}
	if p.Status != nil {
		st := *p.Status
		if !st.Valid() {
			return nil, nil, Invalid("unknown status")
		}
		if st == TaskInProgress && before.Status != TaskInProgress {
			return nil, nil, Invalid("IN_PROGRESS is set by the agent runner; move the task to TODO to start it")
		}
		if st != before.Status {
			add("status", string(st))
			if st != TaskInProgress {
				sets = append(sets, "lease_until=NULL")
			}
			if st == TaskDone || st == TaskReview {
				sets = append(sets, "completed_at=NOW()")
			}
		}
	}
	if p.Priority != nil {
		if !validPriority(*p.Priority) {
			return nil, nil, Invalid("priority must be low, normal, high or urgent")
		}
		add("priority", *p.Priority)
	}
	if p.Labels != nil {
		add("labels", cleanLabels(*p.Labels))
	}
	if p.AssigneeAgentID != nil {
		if err := sameOrg(ctx, s.pool, before.OrgID, "agents", "assignee", *p.AssigneeAgentID); err != nil {
			return nil, nil, err
		}
		add("assignee_agent_id", *p.AssigneeAgentID)
	}
	if p.DueAt != nil {
		add("due_at", *p.DueAt)
	}
	if p.PublisherConnectorID != nil {
		add("publisher_connector_id", *p.PublisherConnectorID)
	}
	if p.PublishAccounts != nil {
		add("publish_accounts", cleanList(*p.PublishAccounts))
	}
	if p.Position != nil {
		add("position", *p.Position)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE tasks SET "+strings.Join(sets, ", ")+" WHERE id=$1", args...); err != nil {
		return nil, nil, NotFound(err)
	}
	after, err = s.GetTask(ctx, id)
	return before, after, err
}

// SetTaskStatusIf changes status only when the current status is one of from (guards races with
// humans dragging cards while an agent finishes). It reports whether the change happened.
func (s *Store) SetTaskStatusIf(ctx context.Context, id uuid.UUID, from []TaskStatus, to TaskStatus, result *string) (bool, error) {
	fs := make([]string, len(from))
	for i, f := range from {
		fs[i] = string(f)
	}
	completed := to == TaskDone || to == TaskReview
	tag, err := s.pool.Exec(ctx, `UPDATE tasks SET status=$3::text, lease_until=NULL, version=version+1, updated_at=NOW(),
		result = COALESCE($4, result),
		completed_at = CASE WHEN $5 THEN NOW() ELSE completed_at END,
		position = COALESCE((SELECT max(o.position) FROM tasks o WHERE o.status=$3::text AND o.org_id=tasks.org_id), 0) + 1024
		WHERE id=$1 AND status = ANY($2)`, id, fs, string(to), result, completed)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) DeleteTask(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM tasks WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// TaskDepth counts ancestors (0 for a top-level task).
func (s *Store) TaskDepth(ctx context.Context, id uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `WITH RECURSIVE chain AS (
		SELECT id, parent_id, 0 AS depth FROM tasks WHERE id=$1
		UNION ALL SELECT t.id, t.parent_id, c.depth+1 FROM tasks t JOIN chain c ON t.id=c.parent_id WHERE c.depth < 20)
		SELECT max(depth) FROM chain`, id).Scan(&n)
	return n, err
}

// --- runner support ---

// ClaimTodo leases up to limit TODO tasks assigned to active agents, marking them IN_PROGRESS.
func (s *Store) ClaimTodo(ctx context.Context, lease time.Duration, limit int) ([]uuid.UUID, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `UPDATE tasks SET status='IN_PROGRESS', lease_until = NOW() + make_interval(secs => $1),
		attempts = attempts + 1, started_at = COALESCE(started_at, NOW()), updated_at = NOW(), version = version + 1
		WHERE id IN (
			SELECT t.id FROM tasks t JOIN agents a ON a.id=t.assignee_agent_id AND a.is_active
			LEFT JOIN connectors rc ON rc.id = a.runtime_connector_id
			WHERE t.status='TODO' AND COALESCE(rc.type,'') <> 'external'
			ORDER BY CASE t.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END, t.updated_at
			LIMIT $2 FOR UPDATE OF t SKIP LOCKED)
		RETURNING id`, lease.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ExtendTaskLease keeps a running task reserved. It reports false when the task is no longer
// IN_PROGRESS (a human canceled or moved it, possibly through another instance): the worker must stop.
func (s *Store) ExtendTaskLease(ctx context.Context, id uuid.UUID, lease time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE tasks SET lease_until = NOW() + make_interval(secs => $2)
		WHERE id=$1 AND status='IN_PROGRESS'`, id, lease.Seconds())
	return tag.RowsAffected() == 1, err
}

// ExpiredTaskLeases returns IN_PROGRESS tasks whose runner died.
func (s *Store) ExpiredTaskLeases(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM tasks WHERE status='IN_PROGRESS' AND (lease_until IS NULL OR lease_until < NOW())`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// OpenSubtasks counts children that are not finished.
func (s *Store) OpenSubtasks(ctx context.Context, parentID uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE parent_id=$1 AND status NOT IN ('DONE','CANCELED','REVIEW')`, parentID).Scan(&n)
	return n, err
}

// --- messages ---

type TaskMessage struct {
	ID         uuid.UUID       `json:"id"`
	TaskID     uuid.UUID       `json:"task_id"`
	Seq        int64           `json:"seq"`
	AuthorType string          `json:"author_type"`
	AgentID    *uuid.UUID      `json:"agent_id"`
	Agent      *AgentRef       `json:"agent"`
	Kind       string          `json:"kind"`
	Content    string          `json:"content"`
	Data       json.RawMessage `json:"data"`
	CreatedAt  time.Time       `json:"created_at"`
}

func (s *Store) AddTaskMessage(ctx context.Context, taskID uuid.UUID, authorType string, agentID *uuid.UUID, kind, content string, data any) (*TaskMessage, error) {
	var raw []byte
	if data != nil {
		var err error
		if raw, err = json.Marshal(data); err != nil {
			return nil, err
		}
	}
	if kind == "" {
		kind = "message"
	}
	var id uuid.UUID
	if err := s.pool.QueryRow(ctx, `INSERT INTO task_messages (task_id, author_type, agent_id, kind, content, data)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`, taskID, authorType, agentID, kind, content, raw).Scan(&id); err != nil {
		return nil, NotFound(err)
	}
	_, _ = s.pool.Exec(ctx, "UPDATE tasks SET updated_at=NOW() WHERE id=$1", taskID)
	msgs, err := s.taskMessages(ctx, "m.id=$1", id)
	if err != nil || len(msgs) == 0 {
		return nil, err
	}
	return msgs[0], nil
}

func (s *Store) TaskMessages(ctx context.Context, taskID uuid.UUID) ([]*TaskMessage, error) {
	return s.taskMessages(ctx, "m.task_id=$1", taskID)
}

func (s *Store) taskMessages(ctx context.Context, cond string, arg any) ([]*TaskMessage, error) {
	rows, err := s.pool.Query(ctx, `SELECT m.id, m.task_id, m.seq, m.author_type, m.agent_id, a.id, a.name, a.slug, a.avatar,
		m.kind, m.content, m.data, m.created_at
		FROM task_messages m LEFT JOIN agents a ON a.id=m.agent_id WHERE `+cond+` ORDER BY m.seq`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TaskMessage{}
	for rows.Next() {
		var m TaskMessage
		var aID *uuid.UUID
		var aName, aSlug, aAvatar *string
		var data []byte
		if err := rows.Scan(&m.ID, &m.TaskID, &m.Seq, &m.AuthorType, &m.AgentID, &aID, &aName, &aSlug, &aAvatar,
			&m.Kind, &m.Content, &data, &m.CreatedAt); err != nil {
			return nil, err
		}
		if aID != nil {
			m.Agent = &AgentRef{ID: *aID, Name: deref(aName), Slug: deref(aSlug), Avatar: deref(aAvatar)}
		}
		if len(data) > 0 {
			m.Data = data
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// HumanMessageSince reports whether a human commented on the task after t.
func (s *Store) HumanMessageSince(ctx context.Context, taskID uuid.UUID, t time.Time) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM task_messages WHERE task_id=$1 AND author_type='human' AND created_at > $2)`,
		taskID, t).Scan(&ok)
	return ok, err
}

// --- agent runs ---

type AgentRun struct {
	ID           uuid.UUID  `json:"id"`
	TaskID       uuid.UUID  `json:"task_id"`
	AgentID      *uuid.UUID `json:"agent_id"`
	Status       string     `json:"status"`
	Turns        int        `json:"turns"`
	ToolCalls    int        `json:"tool_calls"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	// CacheReadTokens and CacheWriteTokens are the parts of InputTokens served from and written to
	// the provider's prompt cache.
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	Error            string `json:"error"`
	// ConnectorID is the LLM connector (built-in runtime) or the ACP runtime connector used.
	ConnectorID *uuid.UUID `json:"connector_id"`
	Model       string     `json:"model"`
	Runtime     string     `json:"runtime"` // builtin | acp
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

func (s *Store) StartAgentRun(ctx context.Context, taskID, agentID uuid.UUID) (*AgentRun, error) {
	r := &AgentRun{TaskID: taskID, AgentID: &agentID, Status: "RUNNING", Runtime: "builtin"}
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_runs (task_id, agent_id, org_id)
		SELECT $1, $2, org_id FROM tasks WHERE id=$1 RETURNING id, started_at`,
		taskID, agentID).Scan(&r.ID, &r.StartedAt)
	return r, err
}

func (s *Store) FinishAgentRun(ctx context.Context, r *AgentRun) error {
	runtime := r.Runtime
	if runtime == "" {
		runtime = "builtin"
	}
	_, err := s.pool.Exec(ctx, `UPDATE agent_runs SET status=$2, turns=$3, tool_calls=$4, input_tokens=$5, output_tokens=$6,
		error=$7, connector_id=$8, model=$9, runtime=$10, cache_read_tokens=$11, cache_write_tokens=$12, finished_at=NOW()
		WHERE id=$1`,
		r.ID, r.Status, r.Turns, r.ToolCalls, r.InputTokens, r.OutputTokens, r.Error, r.ConnectorID, r.Model, runtime,
		r.CacheReadTokens, r.CacheWriteTokens)
	return err
}

func (s *Store) ListAgentRuns(ctx context.Context, taskID uuid.UUID) ([]*AgentRun, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, task_id, agent_id, status, turns, tool_calls, input_tokens, output_tokens,
		cache_read_tokens, cache_write_tokens, error, connector_id, model, runtime, started_at, finished_at
		FROM agent_runs WHERE task_id=$1
		ORDER BY started_at DESC LIMIT 50`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AgentRun{}
	for rows.Next() {
		var r AgentRun
		if err := rows.Scan(&r.ID, &r.TaskID, &r.AgentID, &r.Status, &r.Turns, &r.ToolCalls, &r.InputTokens,
			&r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens, &r.Error, &r.ConnectorID, &r.Model, &r.Runtime, &r.StartedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// FailStaleAgentRuns marks runs left RUNNING by a previous process.
func (s *Store) FailStaleAgentRuns(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_runs SET status='FAILED', error='interrupted (server restarted)', finished_at=NOW()
		WHERE status='RUNNING' AND runtime <> 'external'`)
	return err
}

// --- schedules ---

type Schedule struct {
	ID              uuid.UUID  `json:"id"`
	OrgID           uuid.UUID  `json:"org_id"`
	Name            string     `json:"name"`
	CronExpr        string     `json:"cron_expr"`
	Timezone        string     `json:"timezone"`
	IsActive        bool       `json:"is_active"`
	AssigneeAgentID *uuid.UUID `json:"assignee_agent_id"`
	TaskTitle       string     `json:"task_title"`
	TaskDescription string     `json:"task_description"`
	Priority        string     `json:"priority"`
	CreatedBy       string     `json:"created_by"`
	CreatorAgentID  *uuid.UUID `json:"creator_agent_id"`
	// Publishing defaults copied to the tasks this schedule creates.
	PublisherConnectorID *uuid.UUID `json:"publisher_connector_id"`
	PublishAccounts      []string   `json:"publish_accounts"`
	LastRunAt            *time.Time `json:"last_run_at"`
	NextRunAt            *time.Time `json:"next_run_at"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

const scheduleColumns = `id, name, cron_expr, timezone, is_active, assignee_agent_id, task_title, task_description,
	priority, created_by, creator_agent_id, last_run_at, created_at, updated_at, publisher_connector_id, publish_accounts, org_id`

func scanSchedule(row pgx.Row) (*Schedule, error) {
	var s Schedule
	err := row.Scan(&s.ID, &s.Name, &s.CronExpr, &s.Timezone, &s.IsActive, &s.AssigneeAgentID, &s.TaskTitle,
		&s.TaskDescription, &s.Priority, &s.CreatedBy, &s.CreatorAgentID, &s.LastRunAt, &s.CreatedAt, &s.UpdatedAt,
		&s.PublisherConnectorID, &s.PublishAccounts, &s.OrgID)
	if s.PublishAccounts == nil {
		s.PublishAccounts = []string{}
	}
	return &s, err
}

func (sc *Schedule) normalize(validate CronValidator) error {
	sc.Name = strings.TrimSpace(sc.Name)
	sc.TaskTitle = strings.TrimSpace(sc.TaskTitle)
	if sc.TaskTitle == "" {
		return Invalid("task title is required")
	}
	if sc.Name == "" {
		sc.Name = sc.TaskTitle
	}
	if sc.Timezone == "" {
		sc.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(sc.Timezone); err != nil {
		return Invalid(fmt.Sprintf("unknown timezone %q", sc.Timezone))
	}
	if sc.Priority == "" {
		sc.Priority = "normal"
	}
	if !validPriority(sc.Priority) {
		return Invalid("priority must be low, normal, high or urgent")
	}
	if sc.CreatedBy == "" {
		sc.CreatedBy = "human"
	}
	sc.CronExpr = strings.TrimSpace(sc.CronExpr)
	if validate != nil {
		if err := validate(sc.CronExpr, sc.Timezone); err != nil {
			return Invalid(fmt.Sprintf("invalid schedule: %v", err))
		}
	}
	return nil
}

// ListSchedules returns an org's schedules, or every org's when org is nil (the scheduler).
func (s *Store) ListSchedules(ctx context.Context, org *uuid.UUID) ([]*Schedule, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+scheduleColumns+" FROM schedules WHERE $1::uuid IS NULL OR org_id=$1 ORDER BY created_at", org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) GetSchedule(ctx context.Context, id uuid.UUID) (*Schedule, error) {
	sc, err := scanSchedule(s.pool.QueryRow(ctx, "SELECT "+scheduleColumns+" FROM schedules WHERE id=$1", id))
	return sc, NotFound(err)
}

func (s *Store) CreateSchedule(ctx context.Context, sc *Schedule, validate CronValidator) (*Schedule, error) {
	if err := sc.normalize(validate); err != nil {
		return nil, err
	}
	if sc.OrgID == uuid.Nil {
		return nil, Invalid("org is required")
	}
	if err := sameOrg(ctx, s.pool, sc.OrgID, "agents", "assignee", sc.AssigneeAgentID); err != nil {
		return nil, err
	}
	return scanSchedule(s.pool.QueryRow(ctx, `INSERT INTO schedules (name, cron_expr, timezone, is_active, assignee_agent_id,
		task_title, task_description, priority, created_by, creator_agent_id, publisher_connector_id, publish_accounts, org_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING `+scheduleColumns,
		truncate(sc.Name, 128), sc.CronExpr, sc.Timezone, sc.IsActive, sc.AssigneeAgentID, truncate(sc.TaskTitle, 255),
		sc.TaskDescription, sc.Priority, sc.CreatedBy, sc.CreatorAgentID, sc.PublisherConnectorID, cleanList(sc.PublishAccounts), sc.OrgID))
}

func (s *Store) UpdateSchedule(ctx context.Context, sc *Schedule, validate CronValidator) (*Schedule, error) {
	if err := sc.normalize(validate); err != nil {
		return nil, err
	}
	org, err := orgOf(ctx, s.pool, "schedules", sc.ID)
	if err != nil {
		return nil, err
	}
	if err := sameOrg(ctx, s.pool, org, "agents", "assignee", sc.AssigneeAgentID); err != nil {
		return nil, err
	}
	out, err := scanSchedule(s.pool.QueryRow(ctx, `UPDATE schedules SET name=$2, cron_expr=$3, timezone=$4, is_active=$5,
		assignee_agent_id=$6, task_title=$7, task_description=$8, priority=$9, publisher_connector_id=$10,
		publish_accounts=$11, next_run_at=NULL, updated_at=NOW()
		WHERE id=$1 RETURNING `+scheduleColumns, sc.ID, truncate(sc.Name, 128), sc.CronExpr, sc.Timezone, sc.IsActive,
		sc.AssigneeAgentID, truncate(sc.TaskTitle, 255), sc.TaskDescription, sc.Priority, sc.PublisherConnectorID,
		cleanList(sc.PublishAccounts)))
	return out, NotFound(err)
}

func (s *Store) DeleteSchedule(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM schedules WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// SpawnScheduledTask creates the task for one schedule firing.
func (s *Store) SpawnScheduledTask(ctx context.Context, scheduleID uuid.UUID) (*Task, error) {
	sc, err := s.GetSchedule(ctx, scheduleID)
	if err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx, "UPDATE schedules SET last_run_at=NOW() WHERE id=$1", scheduleID); err != nil {
		return nil, err
	}
	createdBy := "human"
	if sc.CreatedBy == "agent" {
		createdBy = "agent"
	}
	desc := sc.TaskDescription
	if desc != "" {
		desc += "\n\n"
	}
	desc += fmt.Sprintf("(Created by schedule %q.)", sc.Name)
	return s.CreateTask(ctx, NewTask{
		Title: fmt.Sprintf("%s · %s", sc.TaskTitle, time.Now().Format("2006-01-02")), Description: desc,
		OrgID: sc.OrgID, Priority: sc.Priority, AssigneeAgentID: sc.AssigneeAgentID, CreatedBy: createdBy,
		CreatorAgentID: sc.CreatorAgentID, ScheduleID: &sc.ID, Labels: []string{"scheduled"},
		PublisherConnectorID: sc.PublisherConnectorID, PublishAccounts: sc.PublishAccounts,
	})
}

func cleanLabels(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, l := range in {
		l = strings.TrimSpace(l)
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, truncate(l, 32))
		}
	}
	return out
}

// cleanList trims entries, drops blanks and duplicates, and never returns nil.
func cleanList(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
