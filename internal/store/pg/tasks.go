package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

const taskColumns = `t.id, t.number, t.title, t.description, t.status, t.priority, t.labels, t.assignee_agent_id,
	a.id, a.name, a.slug, a.avatar, t.created_by, t.creator_agent_id, t.parent_id, t.schedule_id,
	(SELECT count(*) FROM tasks c WHERE c.parent_id=t.id),
	(SELECT count(*) FROM tasks c WHERE c.parent_id=t.id AND c.status IN ('DONE','CANCELED','REVIEW')),
	t.result, t.due_at, t.position, (t.status='IN_PROGRESS' AND t.lease_until > NOW()), t.attempts, t.version,
	t.created_at, t.updated_at, t.started_at, t.completed_at, t.publisher_connector_id, t.publish_accounts, t.org_id`

const taskFrom = ` FROM tasks t LEFT JOIN agents a ON a.id = t.assignee_agent_id`

func scanTask(row pgx.Row) (*store.Task, error) {
	var t store.Task
	var status string
	var aID *uuid.UUID
	var aName, aSlug, aAvatar *string
	if err := row.Scan(&t.ID, &t.Number, &t.Title, &t.Description, &status, &t.Priority, &t.Labels, &t.AssigneeAgentID,
		&aID, &aName, &aSlug, &aAvatar, &t.CreatedBy, &t.CreatorAgentID, &t.ParentID, &t.ScheduleID,
		&t.SubtaskCounts.Total, &t.SubtaskCounts.Done, &t.Result, &t.DueAt, &t.Position, &t.Running, &t.Attempts,
		&t.Version, &t.CreatedAt, &t.UpdatedAt, &t.StartedAt, &t.CompletedAt, &t.PublisherConnectorID, &t.PublishAccounts, &t.OrgID); err != nil {
		return nil, err
	}
	t.Status = store.TaskStatus(status)
	if t.PublishAccounts == nil {
		t.PublishAccounts = []string{}
	}
	if t.Labels == nil {
		t.Labels = []string{}
	}
	if aID != nil {
		t.Assignee = &store.AgentRef{ID: *aID, Name: deref(aName), Slug: deref(aSlug), Avatar: deref(aAvatar)}
	}
	return &t, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (s *Store) GetTask(ctx context.Context, id uuid.UUID) (*store.Task, error) {
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
func (s *Store) GetTaskByNumber(ctx context.Context, org uuid.UUID, n int64) (*store.Task, error) {
	t, err := scanTask(s.pool.QueryRow(ctx, "SELECT "+taskColumns+taskFrom+" WHERE t.org_id=$1 AND t.number=$2", org, n))
	return t, NotFound(err)
}

// taskWhere builds the WHERE clause of a task query; statuses=false leaves f.Statuses out (for CountTasks).
func taskWhere(f store.TaskFilter, statuses bool) (string, []any) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.OrgID != nil {
		where = append(where, "t.org_id = "+arg(*f.OrgID))
	}
	if statuses && len(f.Statuses) > 0 {
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
		match := "t.title ILIKE " + p + " OR t.description ILIKE " + p
		// "#12" or "12" also finds task number 12.
		if n, err := strconv.ParseInt(strings.TrimPrefix(q, "#"), 10, 64); err == nil {
			match += " OR t.number = " + arg(n)
		}
		where = append(where, "("+match+")")
	}
	if !f.All {
		where = append(where, "NOT (t.status IN ('DONE','CANCELED') AND t.updated_at < NOW() - INTERVAL '14 days')")
	}
	if len(where) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

func (s *Store) ListTasks(ctx context.Context, f store.TaskFilter) ([]*store.Task, error) {
	cond, args := taskWhere(f, true)
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	order := " ORDER BY t.position, t.created_at, t.id"
	if f.Sort == store.TaskSortRecent {
		order = " ORDER BY COALESCE(t.completed_at, t.updated_at) DESC, t.id DESC"
	}
	args = append(args, limit, max(f.Offset, 0))
	rows, err := s.pool.Query(ctx, "SELECT "+taskColumns+taskFrom+cond+order+
		fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*store.Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CountTasks counts the tasks f matches per status (every status present, 0 when empty), ignoring
// f.Statuses, Limit, Offset and Sort.
func (s *Store) CountTasks(ctx context.Context, f store.TaskFilter) (map[store.TaskStatus]int, error) {
	cond, args := taskWhere(f, false)
	rows, err := s.pool.Query(ctx, "SELECT t.status, count(*) FROM tasks t"+cond+" GROUP BY t.status", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[store.TaskStatus]int, len(store.TaskStatuses))
	for _, st := range store.TaskStatuses {
		out[st] = 0
	}
	for rows.Next() {
		var st store.TaskStatus
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

func (s *Store) CreateTask(ctx context.Context, in store.NewTask) (*store.Task, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return nil, store.Invalid("title is required")
	}
	if in.Priority == "" {
		in.Priority = "normal"
	}
	if !store.ValidPriority(in.Priority) {
		return nil, store.Invalid("priority must be low, normal, high or urgent")
	}
	if in.Status == "" {
		in.Status = store.TaskBacklog
		if in.AssigneeAgentID != nil {
			in.Status = store.TaskTodo
		}
	}
	if !in.Status.Valid() || in.Status == store.TaskInProgress {
		return nil, store.Invalid("invalid initial status")
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
func (s *Store) taskOrg(ctx context.Context, q querier, in store.NewTask) (uuid.UUID, error) {
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

// UpdateTask applies a human edit. It returns the task before and after the change.
func (s *Store) UpdateTask(ctx context.Context, id uuid.UUID, p store.TaskPatch) (before, after *store.Task, err error) {
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
			return nil, nil, store.Invalid("title is required")
		}
		add("title", truncate(t, 255))
	}
	if p.Description != nil {
		add("description", *p.Description)
	}
	resetRecover := false
	if p.Status != nil {
		st := *p.Status
		if !st.Valid() {
			return nil, nil, store.Invalid("unknown status")
		}
		if st == store.TaskInProgress && before.Status != store.TaskInProgress {
			return nil, nil, store.Invalid("IN_PROGRESS is set by the agent runner; move the task to TODO to start it")
		}
		// A human move (or Run now) ends an auto-recover backoff and starts its count again; resending
		// the current status is not a move.
		if st != before.Status || p.RunNow {
			resetRecover = true
		}
		if st != before.Status {
			add("status", string(st))
			if st != store.TaskInProgress {
				sets = append(sets, "lease_until=NULL")
			}
			if st == store.TaskDone || st == store.TaskReview {
				sets = append(sets, "completed_at=NOW()")
			}
		}
	}
	if p.Priority != nil {
		if !store.ValidPriority(*p.Priority) {
			return nil, nil, store.Invalid("priority must be low, normal, high or urgent")
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
		// A new assignee doesn't wait out the old one's backoff, and starts its own count.
		if !store.SameID(before.AssigneeAgentID, *p.AssigneeAgentID) {
			resetRecover = true
		}
	}
	if resetRecover {
		sets = append(sets, "retry_at=NULL", "recover_reset_at=NOW()")
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
func (s *Store) SetTaskStatusIf(ctx context.Context, id uuid.UUID, from []store.TaskStatus, to store.TaskStatus, result *string) (bool, error) {
	return s.setTaskStatus(ctx, id, from, nil, to, result)
}

// SetClaimedTaskStatus is how the holder of a claim finishes it: the task must still be IN_PROGRESS
// under that claim (see Claim), so a run whose task was claimed again since can't move it.
func (s *Store) SetClaimedTaskStatus(ctx context.Context, id, runID uuid.UUID, to store.TaskStatus, result *string) (bool, error) {
	return s.setTaskStatus(ctx, id, []store.TaskStatus{store.TaskInProgress}, &runID, to, result)
}

// setTaskStatus is SetTaskStatusIf, fenced by the claim's run id unless it is nil.
func (s *Store) setTaskStatus(ctx context.Context, id uuid.UUID, from []store.TaskStatus, runID *uuid.UUID, to store.TaskStatus, result *string) (bool, error) {
	fs := make([]string, len(from))
	for i, f := range from {
		fs[i] = string(f)
	}
	completed := to == store.TaskDone || to == store.TaskReview
	tag, err := s.pool.Exec(ctx, `UPDATE tasks SET status=$3::text, lease_until=NULL, retry_at=NULL, version=version+1, updated_at=NOW(),
		result = COALESCE($4, result),
		completed_at = CASE WHEN $5 THEN NOW() ELSE completed_at END,
		position = COALESCE((SELECT max(o.position) FROM tasks o WHERE o.status=$3::text AND o.org_id=tasks.org_id), 0) + 1024
		WHERE id=$1 AND status = ANY($2) AND ($6::uuid IS NULL OR run_id = $6)`, id, fs, string(to), result, completed, runID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// RequeueClaimedTask is how the holder of a claim sends its task back to TODO after a transient
// failure (auto-recover): ClaimTodo skips it until after has passed. Fenced like SetClaimedTaskStatus.
func (s *Store) RequeueClaimedTask(ctx context.Context, id, runID uuid.UUID, after time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE tasks SET status='TODO', lease_until=NULL, retry_at=NOW() + make_interval(secs => $3),
		version=version+1, updated_at=NOW(),
		position = COALESCE((SELECT max(o.position) FROM tasks o WHERE o.status='TODO' AND o.org_id=tasks.org_id), 0) + 1024
		WHERE id=$1 AND status='IN_PROGRESS' AND run_id=$2`, id, runID, after.Seconds())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ClearTaskRetry makes a TODO task waiting out an auto-recover backoff claimable now; false if it
// wasn't waiting.
func (s *Store) ClearTaskRetry(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE tasks SET retry_at=NULL WHERE id=$1 AND retry_at IS NOT NULL`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// RecoverResetAt is when a human last moved the task (see UpdateTask); nil if never.
func (s *Store) RecoverResetAt(ctx context.Context, id uuid.UUID) (*time.Time, error) {
	var at *time.Time
	err := s.pool.QueryRow(ctx, `SELECT recover_reset_at FROM tasks WHERE id=$1`, id).Scan(&at)
	return at, NotFound(err)
}

// DeleteTask removes a task, its subtasks (by cascade) and the bytes of all their files: the cascade
// removes the file rows, but blobs outside the database would be left behind. The rows go in one
// transaction and the blobs once it has committed, so no transaction waits on the blob store; a blob
// that can't be deleted then is left behind, and the error names its file.
func (s *Store) DeleteTask(ctx context.Context, id uuid.UUID) error {
	var files []storedBytes
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Delete the file rows first, locking them, so a file written meanwhile waits on the task.
		rows, err := tx.Query(ctx, `WITH RECURSIVE tree AS (
				SELECT id, 0 AS depth FROM tasks WHERE id=$1
				UNION ALL SELECT t.id, tr.depth+1 FROM tasks t JOIN tree tr ON t.parent_id=tr.id WHERE tr.depth < 20
			) DELETE FROM task_files f USING tree WHERE f.task_id = tree.id RETURNING f.id, f.storage`, id)
		if err != nil {
			return err
		}
		files, err = pgx.CollectRows(rows, pgx.RowToStructByPos[storedBytes])
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "DELETE FROM tasks WHERE id=$1", id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := s.dropAllBytes(ctx, files); err != nil {
		return fmt.Errorf("task %s removed, but its files could not be deleted: %w", id, err)
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

// orgClaimLockSpace is the first key of ClaimTodo's per-org advisory locks; the second is the org's.
const orgClaimLockSpace = 0x6f7267 // "org"

// lockOrgClaims takes ClaimTodo's transaction advisory lock of each org, one at a time in key order, so
// two claimers never deadlock. Two orgs whose keys collide share a lock, which only makes their claimers
// take turns.
func lockOrgClaims(ctx context.Context, tx pgx.Tx, orgs []uuid.UUID) error {
	keys := make([]int32, 0, len(orgs))
	for _, id := range orgs {
		h := fnv.New32a()
		h.Write(id[:])
		keys = append(keys, int32(h.Sum32()))
	}
	slices.Sort(keys)
	for _, k := range slices.Compact(keys) {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, orgClaimLockSpace, k); err != nil {
			return err
		}
	}
	return nil
}

// crewRun picks the tasks the built-in crew works (t, joined to their agent a): every runtime but an
// external agent, which drives itself.
const crewRun = `JOIN agents a ON a.id = t.assignee_agent_id
	LEFT JOIN connectors rc ON rc.id = a.runtime_connector_id`
const crewRunWhere = `COALESCE(rc.type, '') <> 'external'`

// claimable leaves out a TODO task t still waiting out an auto-recover backoff.
const claimable = `(t.retry_at IS NULL OR t.retry_at <= NOW())`

// ClaimTodo leases up to limit TODO tasks assigned to active agents, marking them IN_PROGRESS. Each org
// gets no more than its agent_concurrency (orgDefault when unset) minus the tasks the crew already works
// for it, counted across every instance. Claimers take a transaction advisory lock per org they claim
// for (lockOrgClaims) and count after taking it, so two instances never claim
// past an org's limit together: the second waits for the first to commit and then sees its claims. An
// advisory lock, not the org's row, so creating a task (which bumps the org's task number) never waits
// on a claim.
func (s *Store) ClaimTodo(ctx context.Context, lease time.Duration, limit, orgDefault int) ([]store.Claim, error) {
	if limit <= 0 {
		return nil, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT DISTINCT t.org_id FROM tasks t `+crewRun+`
		WHERE t.status='TODO' AND a.is_active AND `+crewRunWhere+` AND `+claimable)
	if err != nil {
		return nil, err
	}
	orgs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil || len(orgs) == 0 {
		return nil, err
	}
	if err := lockOrgClaims(ctx, tx, orgs); err != nil {
		return nil, err
	}
	// A task an inactive agent still works holds its slot until the run ends, so running counts every agent.
	rows, err = tx.Query(ctx, `WITH room AS (
			SELECT o.id AS org_id, COALESCE(o.agent_concurrency, $4) - (
				SELECT count(*) FROM tasks t `+crewRun+` WHERE t.org_id = o.id AND t.status='IN_PROGRESS' AND `+crewRunWhere+`) AS free
			FROM orgs o WHERE o.id = ANY($3)
		), todo AS (
			SELECT t.id, t.org_id, t.updated_at,
				CASE t.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END AS prio
			FROM tasks t `+crewRun+` WHERE t.org_id = ANY($3) AND t.status='TODO' AND a.is_active AND `+crewRunWhere+` AND `+claimable+`
		), ranked AS (
			SELECT todo.*, row_number() OVER (PARTITION BY org_id ORDER BY prio, updated_at) AS n FROM todo
		), picked AS (
			SELECT r.id FROM ranked r JOIN room USING (org_id) WHERE r.n <= room.free ORDER BY r.prio, r.updated_at LIMIT $2
		), locked AS (
			-- A task another writer holds is skipped, not waited on: this claim holds its orgs' locks.
			SELECT id FROM tasks WHERE id IN (SELECT id FROM picked) AND status='TODO' FOR UPDATE SKIP LOCKED
		)
		UPDATE tasks SET status='IN_PROGRESS', lease_until = NOW() + make_interval(secs => $1), retry_at = NULL,
			attempts = attempts + 1, run_id = gen_random_uuid(),
			started_at = COALESCE(started_at, NOW()), updated_at = NOW(), version = version + 1
		WHERE id IN (SELECT id FROM locked)
		RETURNING id, run_id`, lease.Seconds(), limit, orgs, orgDefault)
	if err != nil {
		return nil, err
	}
	claims, err := pgx.CollectRows(rows, pgx.RowToStructByPos[store.Claim])
	if err != nil {
		return nil, err
	}
	return claims, tx.Commit(ctx)
}

// ExtendTaskLease keeps a running task reserved for the claim's run. It reports false when the
// task is no longer IN_PROGRESS under that claim (a human canceled or moved it, possibly through
// another instance, or it was claimed again after the lease ran out): the worker must stop.
func (s *Store) ExtendTaskLease(ctx context.Context, id, runID uuid.UUID, lease time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE tasks SET lease_until = NOW() + make_interval(secs => $2)
		WHERE id=$1 AND status='IN_PROGRESS' AND run_id=$3`, id, lease.Seconds(), runID)
	return tag.RowsAffected() == 1, err
}

// ExtendAgentTaskLease keeps a self-driven agent's task reserved. It is not fenced by a claim: the
// claim belongs to the agent, not to a process that could stall and come back.
func (s *Store) ExtendAgentTaskLease(ctx context.Context, id uuid.UUID, lease time.Duration) error {
	_, err := s.pool.Exec(ctx, `UPDATE tasks SET lease_until = NOW() + make_interval(secs => $2)
		WHERE id=$1 AND status='IN_PROGRESS'`, id, lease.Seconds())
	return err
}

// TaskRunID is the run of the task's latest claim (see Claim); nil if no claim recorded one.
func (s *Store) TaskRunID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
	var runID *uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT run_id FROM tasks WHERE id=$1`, id).Scan(&runID)
	return runID, NotFound(err)
}

// HoldsClaim reports whether runID may still act on the task: it is IN_PROGRESS, and its claim (see
// Claim) is runID's. A claim with no run recorded (run_id NULL) was made by a version from before
// claims recorded their run: nothing tells its runs apart, so it holds for any of them, as before
// (the lease and the status still fence them). Such a claim must not stop a healthy run working it
// while that version is replaced; resumeRun adopts it, after which it is fenced. A task that is gone
// is held by no one.
func (s *Store) HoldsClaim(ctx context.Context, id, runID uuid.UUID) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(status = 'IN_PROGRESS' AND (run_id IS NULL OR run_id = $2), false)
		FROM tasks WHERE id=$1`, id, runID).Scan(&ok)
	if NotFound(err) == domain.ErrNotFound {
		return false, nil
	}
	return ok, err
}

// AdoptTaskRun records a running run as its task's claim when the claim recorded none (it was made
// by a version that didn't), so the run is fenced like any other from then on.
func (s *Store) AdoptTaskRun(ctx context.Context, id, runID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE tasks SET run_id=$2 WHERE id=$1 AND run_id IS NULL AND status='IN_PROGRESS'`, id, runID)
	return err
}

// ExpiredTaskLeases returns IN_PROGRESS tasks whose runner died.
func (s *Store) ExpiredTaskLeases(ctx context.Context) ([]store.ExpiredLease, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, run_id FROM tasks WHERE status='IN_PROGRESS' AND (lease_until IS NULL OR lease_until < NOW())`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ExpiredLease
	for rows.Next() {
		var l store.ExpiredLease
		if err := rows.Scan(&l.ID, &l.RunID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// OpenSubtasks counts children that are not finished.
func (s *Store) OpenSubtasks(ctx context.Context, parentID uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE parent_id=$1 AND status NOT IN ('DONE','CANCELED','REVIEW')`, parentID).Scan(&n)
	return n, err
}

// --- messages ---

func (s *Store) AddTaskMessage(ctx context.Context, taskID uuid.UUID, authorType string, agentID *uuid.UUID, kind, content string, data any) (*store.TaskMessage, error) {
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

func (s *Store) TaskMessages(ctx context.Context, taskID uuid.UUID) ([]*store.TaskMessage, error) {
	return s.taskMessages(ctx, "m.task_id=$1", taskID)
}

func (s *Store) taskMessages(ctx context.Context, cond string, arg any) ([]*store.TaskMessage, error) {
	rows, err := s.pool.Query(ctx, `SELECT m.id, m.task_id, m.seq, m.author_type, m.agent_id, a.id, a.name, a.slug, a.avatar,
		m.kind, m.content, m.data, m.created_at
		FROM task_messages m LEFT JOIN agents a ON a.id=m.agent_id WHERE `+cond+` ORDER BY m.seq`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*store.TaskMessage{}
	for rows.Next() {
		var m store.TaskMessage
		var aID *uuid.UUID
		var aName, aSlug, aAvatar *string
		var data []byte
		if err := rows.Scan(&m.ID, &m.TaskID, &m.Seq, &m.AuthorType, &m.AgentID, &aID, &aName, &aSlug, &aAvatar,
			&m.Kind, &m.Content, &data, &m.CreatedAt); err != nil {
			return nil, err
		}
		if aID != nil {
			m.Agent = &store.AgentRef{ID: *aID, Name: deref(aName), Slug: deref(aSlug), Avatar: deref(aAvatar)}
		}
		if len(data) > 0 {
			m.Data = data
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// HumanMessageSince reports whether a human commented on the task after t.
// LastHumanMessageAt is when a human last wrote in the task's thread; ok is false if never.
func (s *Store) LastHumanMessageAt(ctx context.Context, taskID uuid.UUID) (t time.Time, ok bool, err error) {
	var at *time.Time
	err = s.pool.QueryRow(ctx, `SELECT max(created_at) FROM task_messages WHERE task_id=$1 AND author_type='human'`, taskID).Scan(&at)
	if err != nil || at == nil {
		return time.Time{}, false, err
	}
	return *at, true, nil
}

func (s *Store) HumanMessageSince(ctx context.Context, taskID uuid.UUID, t time.Time) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM task_messages WHERE task_id=$1 AND author_type='human' AND created_at > $2)`,
		taskID, t).Scan(&ok)
	return ok, err
}

// --- agent runs ---

// agentRunColumns and agentRunDest keep every query that loads an AgentRun reading the same columns.
const agentRunColumns = `id, task_id, agent_id, status, turns, tool_calls, input_tokens, output_tokens,
	cache_read_tokens, cache_write_tokens, error, connector_id, model, runtime, started_at, finished_at,
	context_tokens, context_window, outcome`

func agentRunDest(r *store.AgentRun) []any {
	return []any{&r.ID, &r.TaskID, &r.AgentID, &r.Status, &r.Turns, &r.ToolCalls, &r.InputTokens, &r.OutputTokens,
		&r.CacheReadTokens, &r.CacheWriteTokens, &r.Error, &r.ConnectorID, &r.Model, &r.Runtime, &r.StartedAt, &r.FinishedAt,
		&r.ContextTokens, &r.ContextWindow, &r.Outcome}
}

// StartAgentRun opens the run a claim minted (Claim.RunID).
func (s *Store) StartAgentRun(ctx context.Context, runID, taskID, agentID uuid.UUID) (*store.AgentRun, error) {
	r := &store.AgentRun{ID: runID, TaskID: taskID, AgentID: &agentID, Status: "RUNNING", Runtime: "builtin"}
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_runs (id, task_id, agent_id, org_id)
		SELECT $1, $2, $3, org_id FROM tasks WHERE id=$2 RETURNING started_at`,
		runID, taskID, agentID).Scan(&r.StartedAt)
	return r, err
}

func (s *Store) FinishAgentRun(ctx context.Context, r *store.AgentRun) error {
	runtime := r.Runtime
	if runtime == "" {
		runtime = "builtin"
	}
	_, err := s.pool.Exec(ctx, `UPDATE agent_runs SET status=$2, turns=$3, tool_calls=$4, input_tokens=$5, output_tokens=$6,
		error=$7, connector_id=$8, model=$9, runtime=$10, cache_read_tokens=$11, cache_write_tokens=$12,
		context_tokens=$13, context_window=$14, outcome=$15, finished_at=NOW()
		WHERE id=$1`,
		r.ID, r.Status, r.Turns, r.ToolCalls, r.InputTokens, r.OutputTokens, r.Error, r.ConnectorID, r.Model, runtime,
		r.CacheReadTokens, r.CacheWriteTokens, r.ContextTokens, r.ContextWindow, r.Outcome)
	return err
}

// FailAgentRun closes a run that is still recorded as running, whose worker is gone (a lapsed lease).
func (s *Store) FailAgentRun(ctx context.Context, id uuid.UUID, outcome, errText string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_runs SET status='FAILED', outcome=$2, error=$3, finished_at=NOW()
		WHERE id=$1 AND status='RUNNING'`, id, outcome, errText)
	return err
}

// SetRunContext records how full the context is after one of a running run's turns.
func (s *Store) SetRunContext(ctx context.Context, runID uuid.UUID, tokens, window int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_runs SET context_tokens=$2, context_window=$3 WHERE id=$1`, runID, tokens, window)
	return err
}

// MarkRunExternal tags a run as one a self-driven agent works, on the runtime connectorID, so an
// interrupted one is recognizable as such.
func (s *Store) MarkRunExternal(ctx context.Context, runID uuid.UUID, connectorID *uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_runs SET runtime='external', connector_id=$2 WHERE id=$1`, runID, connectorID)
	return err
}

func (s *Store) ListAgentRuns(ctx context.Context, taskID uuid.UUID) ([]*store.AgentRun, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+agentRunColumns+` FROM agent_runs WHERE task_id=$1
		ORDER BY started_at DESC LIMIT 50`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*store.AgentRun{}
	for rows.Next() {
		var r store.AgentRun
		if err := rows.Scan(agentRunDest(&r)...); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// GetAgentRun loads one run.
func (s *Store) GetAgentRun(ctx context.Context, id uuid.UUID) (*store.AgentRun, error) {
	var r store.AgentRun
	err := s.pool.QueryRow(ctx, `SELECT `+agentRunColumns+` FROM agent_runs WHERE id=$1`, id).Scan(agentRunDest(&r)...)
	if err != nil {
		return nil, NotFound(err)
	}
	return &r, nil
}

// HandOverAgentRun marks a running run whose delivery is ending as waiting for the next one to keep
// watching it (outcome "handover"; see ClaimHandedOverRuns).
func (s *Store) HandOverAgentRun(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_runs SET outcome='handover' WHERE id=$1 AND status='RUNNING'`, id)
	return err
}

// ClaimHandedOverRuns takes up to n handed-over runs for this delivery to keep watching; each is
// taken by exactly one caller.
func (s *Store) ClaimHandedOverRuns(ctx context.Context, n int) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `UPDATE agent_runs SET outcome='' WHERE id IN (
		SELECT id FROM agent_runs WHERE status='RUNNING' AND outcome='handover'
		ORDER BY started_at LIMIT $1 FOR UPDATE SKIP LOCKED) RETURNING id`, n)
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

// FailStaleAgentRuns marks runs left RUNNING by a previous process.
func (s *Store) FailStaleAgentRuns(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_runs SET status='FAILED', error='interrupted (server restarted)', finished_at=NOW()
		WHERE status='RUNNING' AND runtime <> 'external'`)
	return err
}

// --- schedules ---

const scheduleColumns = `id, name, cron_expr, timezone, is_active, assignee_agent_id, task_title, task_description,
	priority, created_by, creator_agent_id, last_run_at, created_at, updated_at, publisher_connector_id, publish_accounts, org_id`

func scanSchedule(row pgx.Row) (*store.Schedule, error) {
	var s store.Schedule
	err := row.Scan(&s.ID, &s.Name, &s.CronExpr, &s.Timezone, &s.IsActive, &s.AssigneeAgentID, &s.TaskTitle,
		&s.TaskDescription, &s.Priority, &s.CreatedBy, &s.CreatorAgentID, &s.LastRunAt, &s.CreatedAt, &s.UpdatedAt,
		&s.PublisherConnectorID, &s.PublishAccounts, &s.OrgID)
	if s.PublishAccounts == nil {
		s.PublishAccounts = []string{}
	}
	return &s, err
}

// ListSchedules returns an org's schedules, or every org's when org is nil (the scheduler).
func (s *Store) ListSchedules(ctx context.Context, org *uuid.UUID) ([]*store.Schedule, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+scheduleColumns+" FROM schedules WHERE $1::uuid IS NULL OR org_id=$1 ORDER BY created_at", org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*store.Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) GetSchedule(ctx context.Context, id uuid.UUID) (*store.Schedule, error) {
	sc, err := scanSchedule(s.pool.QueryRow(ctx, "SELECT "+scheduleColumns+" FROM schedules WHERE id=$1", id))
	return sc, NotFound(err)
}

func (s *Store) CreateSchedule(ctx context.Context, sc *store.Schedule, validate store.CronValidator) (*store.Schedule, error) {
	if err := sc.Normalize(validate); err != nil {
		return nil, err
	}
	if sc.OrgID == uuid.Nil {
		return nil, store.Invalid("org is required")
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

func (s *Store) UpdateSchedule(ctx context.Context, sc *store.Schedule, validate store.CronValidator) (*store.Schedule, error) {
	if err := sc.Normalize(validate); err != nil {
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
func (s *Store) SpawnScheduledTask(ctx context.Context, scheduleID uuid.UUID) (*store.Task, error) {
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
	return s.CreateTask(ctx, store.NewTask{
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
