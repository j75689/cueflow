package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/j75689/cueflow/internal/domain"
)

const postColumns = `id, group_id, pipeline_id, run_id, publisher_connector_id, title, content, thread_parts,
	hashtags, image_prompt, platforms, media_urls, status, review_note, source_context, error_message,
	external_post_ids, attempts, version, scheduled_at, published_at, created_at, updated_at`

func scanPost(row pgx.Row) (*domain.Post, error) {
	var p domain.Post
	var status string
	var ext []byte
	if err := row.Scan(&p.ID, &p.GroupID, &p.PipelineID, &p.RunID, &p.PublisherConnectorID, &p.Title, &p.Content,
		&p.ThreadParts, &p.Hashtags, &p.ImagePrompt, &p.Platforms, &p.MediaURLs, &status, &p.ReviewNote,
		&p.SourceContext, &p.ErrorMessage, &ext, &p.Attempts, &p.Version, &p.ScheduledAt, &p.PublishedAt,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Status = domain.Status(status)
	p.ExternalPostIDs = map[string]string{}
	_ = json.Unmarshal(ext, &p.ExternalPostIDs)
	normalizePost(&p)
	p.Warnings = p.ComputeWarnings()
	return &p, nil
}

func normalizePost(p *domain.Post) {
	for _, s := range []*[]string{&p.ThreadParts, &p.Hashtags, &p.Platforms, &p.MediaURLs} {
		if *s == nil {
			*s = []string{}
		}
	}
	if len(p.SourceContext) == 0 {
		p.SourceContext = json.RawMessage(`{}`)
	}
	if p.Warnings == nil {
		p.Warnings = []string{}
	}
}

type PostFilter struct {
	Statuses   []domain.Status
	Query      string
	PipelineID *uuid.UUID
	RunID      *uuid.UUID
	Limit      int
	Offset     int
}

func (s *Store) ListPosts(ctx context.Context, f PostFilter) ([]*domain.Post, int, error) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if len(f.Statuses) > 0 {
		ss := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			ss[i] = string(st)
		}
		where = append(where, "status = ANY("+arg(ss)+")")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		p := arg("%" + q + "%")
		where = append(where, "(title ILIKE "+p+" OR content ILIKE "+p+")")
	}
	if f.PipelineID != nil {
		where = append(where, "pipeline_id = "+arg(*f.PipelineID))
	}
	if f.RunID != nil {
		where = append(where, "run_id = "+arg(*f.RunID))
	}
	cond := ""
	if len(where) > 0 {
		cond = "WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM posts "+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := fmt.Sprintf("SELECT %s FROM posts %s ORDER BY created_at DESC, id LIMIT %s OFFSET %s",
		postColumns, cond, arg(limit), arg(max(f.Offset, 0)))
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	posts := []*domain.Post{}
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, 0, err
		}
		posts = append(posts, p)
	}
	return posts, total, rows.Err()
}

func (s *Store) GetPost(ctx context.Context, id uuid.UUID) (*domain.Post, error) {
	p, err := scanPost(s.pool.QueryRow(ctx, "SELECT "+postColumns+" FROM posts WHERE id=$1", id))
	return p, notFound(err)
}

func getPostForUpdate(ctx context.Context, q querier, id uuid.UUID) (*domain.Post, error) {
	p, err := scanPost(q.QueryRow(ctx, "SELECT "+postColumns+" FROM posts WHERE id=$1 FOR UPDATE", id))
	return p, notFound(err)
}

// NewPost is the input for CreatePost.
type NewPost struct {
	GroupID              *uuid.UUID
	PipelineID           *uuid.UUID
	RunID                *uuid.UUID
	PublisherConnectorID *uuid.UUID
	Title                string
	Content              string
	ThreadParts          []string
	Hashtags             []string
	ImagePrompt          string
	Platforms            []string
	MediaURLs            []string
	SourceContext        any
	ScheduledAt          *time.Time
}

// CreatePost inserts a DRAFT (audited as GENERATED for the agent, CREATED for humans) and, when
// submit is set, moves it on to PENDING_REVIEW in the same transaction.
func (s *Store) CreatePost(ctx context.Context, in NewPost, actor domain.Actor, submit bool) (*domain.Post, error) {
	if strings.TrimSpace(in.Content) == "" && len(in.ThreadParts) == 0 {
		return nil, invalid("content is required")
	}
	if strings.TrimSpace(in.Content) == "" {
		in.Content = strings.Join(in.ThreadParts, "\n\n")
	}
	if err := domain.ValidatePlatforms(in.Platforms); err != nil {
		return nil, invalid(err.Error())
	}
	srcCtx, err := json.Marshal(orEmpty(in.SourceContext))
	if err != nil {
		return nil, err
	}
	var out *domain.Post
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := scanPost(tx.QueryRow(ctx, `INSERT INTO posts (group_id, pipeline_id, run_id, publisher_connector_id,
			title, content, thread_parts, hashtags, image_prompt, platforms, media_urls, source_context, scheduled_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING `+postColumns,
			in.GroupID, in.PipelineID, in.RunID, in.PublisherConnectorID, truncate(in.Title, 255), in.Content,
			nonNil(in.ThreadParts), cleanTags(in.Hashtags), in.ImagePrompt, in.Platforms, nonNil(in.MediaURLs), srcCtx, in.ScheduledAt))
		if err != nil {
			return err
		}
		action := domain.ActionCreated
		if actor == domain.ActorAgent {
			action = domain.ActionGenerated
		}
		if err := insertAudit(ctx, tx, p.ID, action, actor, "", domain.StatusDraft, "", nil); err != nil {
			return err
		}
		if submit {
			if p, err = transition(ctx, tx, p, domain.StatusPendingReview, domain.ActionSubmitted, actor, "", nil); err != nil {
				return err
			}
		}
		out = p
		return nil
	})
	return out, err
}

// PostPatch carries reviewer edits; nil fields are left unchanged.
type PostPatch struct {
	Title                *string
	Content              *string
	ThreadParts          *[]string
	Hashtags             *[]string
	MediaURLs            *[]string
	ImagePrompt          *string
	Platforms            *[]string
	ScheduledAt          **time.Time
	PublisherConnectorID **uuid.UUID
}

// UpdatePost applies an edit with optimistic locking and records the diff in the audit log.
func (s *Store) UpdatePost(ctx context.Context, id uuid.UUID, version int, patch PostPatch) (*domain.Post, error) {
	var out *domain.Post
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := getPostForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return domain.ErrVersionConflict
		}
		if !cur.Status.Editable() {
			return invalid(fmt.Sprintf("posts in %s can no longer be edited", cur.Status))
		}
		next := *cur
		diff := map[string]any{}
		setStr := func(name string, dst *string, v *string) {
			if v != nil && *v != *dst {
				diff[name] = map[string]any{"before": *dst, "after": *v}
				*dst = *v
			}
		}
		setList := func(name string, dst *[]string, v *[]string) {
			if v != nil && !slices.Equal(*dst, nonNil(*v)) {
				diff[name] = map[string]any{"before": *dst, "after": nonNil(*v)}
				*dst = nonNil(*v)
			}
		}
		setStr("title", &next.Title, patch.Title)
		setStr("content", &next.Content, patch.Content)
		setStr("image_prompt", &next.ImagePrompt, patch.ImagePrompt)
		setList("thread_parts", &next.ThreadParts, patch.ThreadParts)
		if patch.Hashtags != nil {
			h := cleanTags(*patch.Hashtags)
			setList("hashtags", &next.Hashtags, &h)
		}
		setList("media_urls", &next.MediaURLs, patch.MediaURLs)
		if patch.Platforms != nil {
			if err := domain.ValidatePlatforms(*patch.Platforms); err != nil {
				return invalid(err.Error())
			}
			setList("platforms", &next.Platforms, patch.Platforms)
		}
		if patch.ScheduledAt != nil && !timeEq(next.ScheduledAt, *patch.ScheduledAt) {
			diff["scheduled_at"] = map[string]any{"before": next.ScheduledAt, "after": *patch.ScheduledAt}
			next.ScheduledAt = *patch.ScheduledAt
		}
		if patch.PublisherConnectorID != nil && !uuidEq(next.PublisherConnectorID, *patch.PublisherConnectorID) {
			diff["publisher_connector_id"] = map[string]any{"before": next.PublisherConnectorID, "after": *patch.PublisherConnectorID}
			next.PublisherConnectorID = *patch.PublisherConnectorID
		}
		if strings.TrimSpace(next.Content) == "" && len(next.ThreadParts) == 0 {
			return invalid("content is required")
		}
		if len(diff) == 0 {
			out = cur
			return nil
		}
		out, err = scanPost(tx.QueryRow(ctx, `UPDATE posts SET title=$2, content=$3, thread_parts=$4, hashtags=$5,
			media_urls=$6, image_prompt=$7, platforms=$8, scheduled_at=$9, publisher_connector_id=$10,
			version=version+1, updated_at=NOW() WHERE id=$1 RETURNING `+postColumns,
			id, truncate(next.Title, 255), next.Content, next.ThreadParts, next.Hashtags, next.MediaURLs,
			next.ImagePrompt, next.Platforms, next.ScheduledAt, next.PublisherConnectorID))
		if err != nil {
			return err
		}
		return insertAudit(ctx, tx, id, domain.ActionEdited, domain.ActorReviewer, cur.Status, cur.Status, "", diff)
	})
	return out, err
}

// TransitionOpts sets extra columns as part of a status change.
type TransitionOpts struct {
	ReviewNote           *string
	ScheduledAt          **time.Time
	PublisherConnectorID **uuid.UUID
	ErrorMessage         *string
	ExternalIDs          map[string]string
	PublishedAt          *time.Time
}

// Transition moves a post to a new status if the state machine allows it, atomically with its
// audit entry.
func (s *Store) Transition(ctx context.Context, id uuid.UUID, to domain.Status, action domain.Action,
	actor domain.Actor, note string, opts *TransitionOpts) (*domain.Post, error) {
	var out *domain.Post
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := getPostForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = transition(ctx, tx, cur, to, action, actor, note, opts)
		return err
	})
	return out, err
}

func transition(ctx context.Context, tx querier, cur *domain.Post, to domain.Status, action domain.Action,
	actor domain.Actor, note string, opts *TransitionOpts) (*domain.Post, error) {
	if err := domain.CheckTransition(cur.Status, to); err != nil {
		return nil, err
	}
	sets := []string{"status=$2", "version=version+1", "updated_at=NOW()"}
	args := []any{cur.ID, string(to)}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s=$%d", col, len(args)))
	}
	if to == domain.StatusQueued {
		sets = append(sets, "lease_until=NULL")
	}
	if opts != nil {
		if opts.ReviewNote != nil {
			add("review_note", *opts.ReviewNote)
		}
		if opts.ScheduledAt != nil {
			add("scheduled_at", *opts.ScheduledAt)
		}
		if opts.PublisherConnectorID != nil {
			add("publisher_connector_id", *opts.PublisherConnectorID)
		}
		if opts.ErrorMessage != nil {
			add("error_message", *opts.ErrorMessage)
		}
		if opts.ExternalIDs != nil {
			raw, _ := json.Marshal(opts.ExternalIDs)
			add("external_post_ids", raw)
		}
		if opts.PublishedAt != nil {
			add("published_at", *opts.PublishedAt)
		}
	}
	p, err := scanPost(tx.QueryRow(ctx, "UPDATE posts SET "+strings.Join(sets, ", ")+" WHERE id=$1 RETURNING "+postColumns, args...))
	if err != nil {
		return nil, err
	}
	if err := insertAudit(ctx, tx, p.ID, action, actor, cur.Status, to, note, nil); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Store) DeletePost(ctx context.Context, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := getPostForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if !cur.Status.Deletable() {
			return invalid(fmt.Sprintf("posts in %s cannot be deleted", cur.Status))
		}
		_, err = tx.Exec(ctx, "DELETE FROM posts WHERE id=$1", id)
		return err
	})
}

func insertAudit(ctx context.Context, q querier, postID uuid.UUID, action domain.Action, actor domain.Actor,
	from, to domain.Status, note string, diff map[string]any) error {
	var raw []byte
	if diff != nil {
		var err error
		if raw, err = json.Marshal(diff); err != nil {
			return err
		}
	}
	_, err := q.Exec(ctx, `INSERT INTO audit_logs (post_id, action, actor, from_status, to_status, note, payload_diff)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, postID, string(action), string(actor), string(from), string(to), note, raw)
	return err
}

func (s *Store) AuditLogs(ctx context.Context, postID uuid.UUID) ([]domain.AuditLog, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, post_id, action, actor, from_status, to_status, note, payload_diff, created_at
		FROM audit_logs WHERE post_id=$1 ORDER BY seq`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AuditLog{}
	for rows.Next() {
		var a domain.AuditLog
		var action, actor, from, to string
		var diff []byte
		if err := rows.Scan(&a.ID, &a.PostID, &action, &actor, &from, &to, &a.Note, &diff, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Action, a.Actor, a.FromStatus, a.ToStatus = domain.Action(action), domain.Actor(actor), domain.Status(from), domain.Status(to)
		if len(diff) > 0 {
			a.PayloadDiff = diff
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CountPostsByStatus(ctx context.Context) (map[domain.Status]int, error) {
	out := map[domain.Status]int{}
	for _, st := range domain.AllStatuses {
		out[st] = 0
	}
	rows, err := s.pool.Query(ctx, "SELECT status, count(*) FROM posts GROUP BY status")
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
		out[domain.Status(st)] = n
	}
	return out, rows.Err()
}

// --- dispatcher support ---

// QueueApproved moves every APPROVED post to QUEUED and returns their IDs.
func (s *Store) QueueApproved(ctx context.Context) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+postColumns+` FROM posts WHERE status='APPROVED'
			ORDER BY updated_at FOR UPDATE SKIP LOCKED LIMIT 100`)
		if err != nil {
			return err
		}
		var posts []*domain.Post
		for rows.Next() {
			p, err := scanPost(rows)
			if err != nil {
				rows.Close()
				return err
			}
			posts = append(posts, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, p := range posts {
			if _, err := transition(ctx, tx, p, domain.StatusQueued, domain.ActionQueued, domain.ActorWorker, "", nil); err != nil {
				return err
			}
			ids = append(ids, p.ID)
		}
		return nil
	})
	return ids, err
}

// ClaimQueued leases up to limit unleased QUEUED posts for publishing.
func (s *Store) ClaimQueued(ctx context.Context, lease time.Duration, limit int) ([]*domain.Post, error) {
	rows, err := s.pool.Query(ctx, `UPDATE posts SET lease_until = NOW() + make_interval(secs => $1),
		attempts = attempts + 1, updated_at = NOW()
		WHERE id IN (SELECT id FROM posts WHERE status='QUEUED' AND lease_until IS NULL
			ORDER BY updated_at LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING `+postColumns, lease.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ExpiredLeases returns QUEUED posts whose worker lease ran out (worker crashed mid-publish).
// They are failed rather than re-published automatically, because the publish may have
// succeeded on the platform before the crash.
func (s *Store) ExpiredLeases(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM posts WHERE status='QUEUED' AND lease_until < NOW()`)
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

// PendingReview returns posts awaiting review, oldest first (for digests).
func (s *Store) PendingReview(ctx context.Context, limit int) ([]*domain.Post, error) {
	posts, _, err := s.ListPosts(ctx, PostFilter{Statuses: []domain.Status{domain.StatusPendingReview}, Limit: limit})
	return posts, err
}

func nonNil(s []string) []string {
	out := make([]string, 0, len(s))
	for _, v := range s {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func cleanTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(t), "#"))
		t = strings.ReplaceAll(t, " ", "")
		if t != "" && !seen[strings.ToLower(t)] {
			seen[strings.ToLower(t)] = true
			out = append(out, t)
		}
	}
	return out
}

func orEmpty(v any) any {
	if v == nil {
		return map[string]any{}
	}
	return v
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func timeEq(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func uuidEq(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// AddAudit records an event that does not change status (e.g. DISPATCHED).
func (s *Store) AddAudit(ctx context.Context, postID uuid.UUID, action domain.Action, actor domain.Actor, note string) error {
	var st string
	if err := s.pool.QueryRow(ctx, "SELECT status FROM posts WHERE id=$1", postID).Scan(&st); err != nil {
		return notFound(err)
	}
	return insertAudit(ctx, s.pool, postID, action, actor, domain.Status(st), domain.Status(st), note, nil)
}
