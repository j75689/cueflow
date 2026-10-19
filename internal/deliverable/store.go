package deliverable

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

// Store runs the module's queries on the core store's pool.
type Store struct {
	*store.Store
}

func NewStore(s *store.Store) *Store { return &Store{Store: s} }

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const columns = `id, org_id, task_id, kind, title, payload, status, review_note, error_message, action_connector_id,
	external_ids, result, attempts, lease_until, version, created_by, acted_at, created_at, updated_at`

func scan(row pgx.Row) (*Deliverable, error) {
	var d Deliverable
	var status, createdBy string
	var ext []byte
	if err := row.Scan(&d.ID, &d.OrgID, &d.TaskID, &d.Kind, &d.Title, &d.Payload, &status, &d.ReviewNote, &d.ErrorMessage,
		&d.ActionConnectorID, &ext, &d.Result, &d.Attempts, &d.LeaseUntil, &d.Version, &createdBy, &d.ActedAt, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	d.Status, d.CreatedBy = Status(status), Actor(createdBy)
	d.ExternalIDs = map[string]string{}
	_ = json.Unmarshal(ext, &d.ExternalIDs)
	if len(d.Payload) == 0 {
		d.Payload = json.RawMessage(`{}`)
	}
	if len(d.Result) == 0 {
		d.Result = json.RawMessage(`{}`)
	}
	return &d, nil
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Deliverable, error) {
	d, err := scan(s.Pool().QueryRow(ctx, "SELECT "+columns+" FROM deliverables WHERE id=$1", id))
	return d, store.NotFound(err)
}

func getForUpdate(ctx context.Context, q querier, id uuid.UUID) (*Deliverable, error) {
	d, err := scan(q.QueryRow(ctx, "SELECT "+columns+" FROM deliverables WHERE id=$1 FOR UPDATE", id))
	return d, store.NotFound(err)
}

// Filter selects deliverables for List.
type Filter struct {
	OrgID    *uuid.UUID
	TaskID   *uuid.UUID
	Kind     string
	Statuses []Status
	Query    string
	Limit    int
	Offset   int
}

func (s *Store) List(ctx context.Context, f Filter) ([]*Deliverable, int, error) {
	where := []string{"TRUE"}
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "$N", fmt.Sprintf("$%d", len(args))))
	}
	if f.OrgID != nil {
		add("org_id=$N", *f.OrgID)
	}
	if f.TaskID != nil {
		add("task_id=$N", *f.TaskID)
	}
	if f.Kind != "" {
		add("kind=$N", f.Kind)
	}
	if len(f.Statuses) > 0 {
		sts := make([]string, 0, len(f.Statuses))
		for _, st := range f.Statuses {
			sts = append(sts, string(st))
		}
		add("status = ANY($N)", sts)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		add("(title ILIKE '%' || $N || '%' OR payload::text ILIKE '%' || $N || '%')", q)
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.Pool().QueryRow(ctx, "SELECT count(*) FROM deliverables WHERE "+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := s.Pool().Query(ctx, fmt.Sprintf("SELECT %s FROM deliverables WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d",
		columns, cond, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Deliverable{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

// New is the input for Create.
type New struct {
	// OrgID may be left empty: it then comes from the task, else the default org.
	OrgID             uuid.UUID
	TaskID            *uuid.UUID
	Kind              string
	Title             string
	Payload           json.RawMessage
	ActionConnectorID *uuid.UUID
}

// Create stores a new item as DRAFT (or PENDING_REVIEW when submit is set) with its first audit entry.
func (s *Store) Create(ctx context.Context, in New, actor Actor, submit bool) (*Deliverable, error) {
	payload, err := validatePayload(in.Kind, in.Payload)
	if err != nil {
		return nil, store.Invalid(err.Error())
	}
	var out *Deliverable
	err = pgx.BeginFunc(ctx, s.Pool(), func(tx pgx.Tx) error {
		var org *uuid.UUID
		if in.OrgID != uuid.Nil {
			org = &in.OrgID
		}
		d, err := scan(tx.QueryRow(ctx, `INSERT INTO deliverables (org_id, task_id, kind, title, payload, action_connector_id, created_by)
			VALUES (COALESCE($1::uuid, (SELECT org_id FROM tasks WHERE id=$2), (SELECT id FROM orgs ORDER BY created_at, id LIMIT 1)),
			$2, $3, $4, $5, $6, $7) RETURNING `+columns,
			org, in.TaskID, strings.TrimSpace(in.Kind), truncate(strings.TrimSpace(in.Title), 255), payload, in.ActionConnectorID, string(actor)))
		if err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, d.ID, ActionCreated, actor, "", StatusDraft, "", nil); err != nil {
			return err
		}
		if submit {
			if d, err = transition(ctx, tx, d, StatusPendingReview, ActionSubmitted, actor, "", nil); err != nil {
				return err
			}
		}
		out = d
		return nil
	})
	return out, err
}

// Patch carries edits; nil fields are left unchanged.
type Patch struct {
	Title             *string
	Payload           json.RawMessage
	ActionConnectorID **uuid.UUID
}

// Update applies an edit with optimistic locking and records the diff in the audit log.
func (s *Store) Update(ctx context.Context, id uuid.UUID, version int, patch Patch, actor Actor, note string) (*Deliverable, error) {
	var out *Deliverable
	err := pgx.BeginFunc(ctx, s.Pool(), func(tx pgx.Tx) error {
		cur, err := getForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return domain.ErrVersionConflict
		}
		if !cur.Status.Editable() {
			return store.Invalid(fmt.Sprintf("deliverables in %s can no longer be edited", cur.Status))
		}
		next := *cur
		diff := map[string]any{}
		if patch.Title != nil && strings.TrimSpace(*patch.Title) != cur.Title {
			next.Title = truncate(strings.TrimSpace(*patch.Title), 255)
			diff["title"] = map[string]any{"before": cur.Title, "after": next.Title}
		}
		if len(patch.Payload) > 0 {
			payload, err := validatePayload(cur.Kind, patch.Payload)
			if err != nil {
				return store.Invalid(err.Error())
			}
			if !jsonEqual(cur.Payload, payload) {
				next.Payload = payload
				diff["payload"] = map[string]any{"before": cur.Payload, "after": payload}
			}
		}
		if patch.ActionConnectorID != nil && !uuidEq(cur.ActionConnectorID, *patch.ActionConnectorID) {
			next.ActionConnectorID = *patch.ActionConnectorID
			diff["action_connector_id"] = map[string]any{"before": cur.ActionConnectorID, "after": next.ActionConnectorID}
		}
		if len(diff) == 0 {
			out = cur
			return nil
		}
		out, err = scan(tx.QueryRow(ctx, `UPDATE deliverables SET title=$2, payload=$3, action_connector_id=$4, version=version+1,
			updated_at=NOW() WHERE id=$1 RETURNING `+columns, id, next.Title, next.Payload, next.ActionConnectorID))
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, id, ActionEdited, actor, cur.Status, cur.Status, note, diff)
	})
	return out, err
}

// TransitionOpts sets extra columns as part of a status change.
type TransitionOpts struct {
	ReviewNote        *string
	ActionConnectorID **uuid.UUID
	ErrorMessage      *string
	ExternalIDs       map[string]string
	Result            map[string]any
	ActedAt           *time.Time
	// Check, when set, vets the current row after the state machine has allowed the move and before it is
	// made; its error aborts the transition.
	Check func(ctx context.Context, cur *Deliverable) error
}

// Transition moves an item to a new status if the state machine allows it, atomically with its
// audit entry.
func (s *Store) Transition(ctx context.Context, id uuid.UUID, to Status, action Action, actor Actor, note string, opts *TransitionOpts) (*Deliverable, error) {
	var out *Deliverable
	err := pgx.BeginFunc(ctx, s.Pool(), func(tx pgx.Tx) error {
		cur, err := getForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = transition(ctx, tx, cur, to, action, actor, note, opts)
		return err
	})
	return out, err
}

func transition(ctx context.Context, tx querier, cur *Deliverable, to Status, action Action, actor Actor, note string, opts *TransitionOpts) (*Deliverable, error) {
	if err := CheckTransition(cur.Status, to); err != nil {
		return nil, err
	}
	if opts != nil && opts.Check != nil {
		if err := opts.Check(ctx, cur); err != nil {
			return nil, err
		}
	}
	sets := []string{"status=$2", "version=version+1", "updated_at=NOW()"}
	args := []any{cur.ID, string(to)}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s=$%d", col, len(args)))
	}
	if to == StatusQueued {
		sets = append(sets, "lease_until=NULL")
	}
	if opts != nil {
		if opts.ReviewNote != nil {
			add("review_note", *opts.ReviewNote)
		}
		if opts.ActionConnectorID != nil {
			add("action_connector_id", *opts.ActionConnectorID)
		}
		if opts.ErrorMessage != nil {
			add("error_message", *opts.ErrorMessage)
		}
		if opts.ExternalIDs != nil {
			raw, _ := json.Marshal(opts.ExternalIDs)
			add("external_ids", raw)
		}
		if opts.Result != nil {
			raw, _ := json.Marshal(opts.Result)
			add("result", raw)
		}
		if opts.ActedAt != nil {
			add("acted_at", *opts.ActedAt)
		}
	}
	d, err := scan(tx.QueryRow(ctx, "UPDATE deliverables SET "+strings.Join(sets, ", ")+" WHERE id=$1 RETURNING "+columns, args...))
	if err != nil {
		return nil, err
	}
	if err := insertAudit(ctx, tx, d.ID, action, actor, cur.Status, to, note, nil); err != nil {
		return nil, err
	}
	return d, nil
}

// Remove drops an item that is still a draft or was rejected.
func (s *Store) Remove(ctx context.Context, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.Pool(), func(tx pgx.Tx) error {
		cur, err := getForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if !cur.Status.Removable() {
			return store.Invalid(fmt.Sprintf("deliverables in %s cannot be removed", cur.Status))
		}
		_, err = tx.Exec(ctx, "DELETE FROM deliverables WHERE id=$1", id)
		return err
	})
}

func insertAudit(ctx context.Context, q querier, id uuid.UUID, action Action, actor Actor, from, to Status, note string, diff map[string]any) error {
	var raw []byte
	if diff != nil {
		var err error
		if raw, err = json.Marshal(diff); err != nil {
			return err
		}
	}
	_, err := q.Exec(ctx, `INSERT INTO deliverable_audit_logs (deliverable_id, action, actor, from_status, to_status, note, payload_diff)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, string(action), string(actor), string(from), string(to), note, raw)
	return err
}

// AddAudit records an entry that is not a status change (the worker's "started via X", say).
func (s *Store) AddAudit(ctx context.Context, id uuid.UUID, action Action, actor Actor, note string) error {
	d, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	return insertAudit(ctx, s.Pool(), id, action, actor, d.Status, d.Status, note, nil)
}

func (s *Store) AuditLogs(ctx context.Context, id uuid.UUID) ([]AuditLog, error) {
	rows, err := s.Pool().Query(ctx, `SELECT id, deliverable_id, action, actor, from_status, to_status, note, payload_diff, created_at
		FROM deliverable_audit_logs WHERE deliverable_id=$1 ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditLog{}
	for rows.Next() {
		var a AuditLog
		var action, actor, from, to string
		var diff []byte
		if err := rows.Scan(&a.ID, &a.DeliverableID, &action, &actor, &from, &to, &a.Note, &diff, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Action, a.Actor, a.FromStatus, a.ToStatus = Action(action), Actor(actor), Status(from), Status(to)
		if len(diff) > 0 {
			a.PayloadDiff = diff
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Ref is what a task shows of its deliverables.
type Ref struct {
	ID     uuid.UUID `json:"id"`
	Kind   string    `json:"kind"`
	Title  string    `json:"title"`
	Status Status    `json:"status"`
}

func (s *Store) ForTask(ctx context.Context, taskID uuid.UUID) ([]Ref, error) {
	rows, err := s.Pool().Query(ctx, `SELECT id, kind, title, status FROM deliverables WHERE task_id=$1 ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ref{}
	for rows.Next() {
		var r Ref
		var st string
		if err := rows.Scan(&r.ID, &r.Kind, &r.Title, &st); err != nil {
			return nil, err
		}
		r.Status = Status(st)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountByStatus counts an org's deliverables per status.
func (s *Store) CountByStatus(ctx context.Context, org uuid.UUID) (map[Status]int, error) {
	out := map[Status]int{}
	for _, st := range AllStatuses {
		out[st] = 0
	}
	rows, err := s.Pool().Query(ctx, "SELECT status, count(*) FROM deliverables WHERE org_id=$1 GROUP BY status", org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[Status(st)] = n
	}
	return out, rows.Err()
}

// --- worker support ---

// QueueApproved moves every APPROVED item to QUEUED and returns them as they were before the move.
func (s *Store) QueueApproved(ctx context.Context) ([]*Deliverable, error) {
	var queued []*Deliverable
	err := pgx.BeginFunc(ctx, s.Pool(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+columns+` FROM deliverables WHERE status='APPROVED' ORDER BY updated_at FOR UPDATE SKIP LOCKED LIMIT 100`)
		if err != nil {
			return err
		}
		var items []*Deliverable
		for rows.Next() {
			d, err := scan(rows)
			if err != nil {
				rows.Close()
				return err
			}
			items = append(items, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, d := range items {
			if _, err := transition(ctx, tx, d, StatusQueued, ActionQueued, ActorWorker, "", nil); err != nil {
				return err
			}
			queued = append(queued, d)
		}
		return nil
	})
	return queued, err
}

// ClaimQueued leases up to limit unleased QUEUED items.
func (s *Store) ClaimQueued(ctx context.Context, lease time.Duration, limit int) ([]*Deliverable, error) {
	rows, err := s.Pool().Query(ctx, `UPDATE deliverables SET lease_until = NOW() + make_interval(secs => $1),
		attempts = attempts + 1, updated_at = NOW()
		WHERE id IN (SELECT id FROM deliverables WHERE status='QUEUED' AND lease_until IS NULL
			ORDER BY updated_at LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING `+columns, lease.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Deliverable
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ExpiredLeases returns QUEUED items whose worker lease ran out.
func (s *Store) ExpiredLeases(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.Pool().Query(ctx, `SELECT id FROM deliverables WHERE status='QUEUED' AND lease_until < NOW()`)
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

// ActionableCount is how many items wait for the worker (approved, or queued without a lease).
func (s *Store) ActionableCount(ctx context.Context) (int, error) {
	var n int
	err := s.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM deliverables WHERE status='APPROVED' OR (status='QUEUED' AND lease_until IS NULL)`).Scan(&n)
	return n, err
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func uuidEq(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return slices.Equal(a, b)
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return string(ax) == string(by)
}
