package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

const runnerCols = `id, name, labels, org_ids, is_active, max_concurrency, version, platform, last_seen_at, created_at`

func fixRunner(r *store.Runner) {
	if r.Labels == nil {
		r.Labels = []string{}
	}
	if r.OrgIDs == nil {
		r.OrgIDs = []uuid.UUID{}
	}
	r.Online = r.IsActive && r.LastSeenAt != nil && time.Since(*r.LastSeenAt) < store.RunnerOnlineWindow
}

func scanRunner(row Row) (*store.Runner, error) {
	r := &store.Runner{}
	err := row.Scan(&r.ID, &r.Name, &r.Labels, &r.OrgIDs, &r.IsActive, &r.MaxConcurrency, &r.Version, &r.Platform,
		&r.LastSeenAt, &r.CreatedAt)
	if err != nil {
		return nil, NotFound(err)
	}
	fixRunner(r)
	return r, nil
}

// CreateRegistration makes a one-time token a new runner trades for its own credential.
func (s *Store) CreateRegistration(ctx context.Context, labels []string, orgIDs []uuid.UUID, ttl time.Duration) (string, time.Time, error) {
	plain, hash := store.NewToken(store.RegistrationTokenPrefix)
	if orgIDs == nil {
		orgIDs = []uuid.UUID{}
	}
	exp := time.Now().Add(ttl)
	_, err := s.w.Exec(ctx, `INSERT INTO runner_registrations (token_hash, labels, org_ids, expires_at, id) VALUES ($1,$2,$3,$4,$5)`,
		hash, store.CleanLabels(labels), orgIDs, exp, uuid.New())
	return plain, exp, err
}

// RegisterRunner consumes a registration token and creates the runner. It returns the runner's
// token (shown once). The labels and orgs the admin chose at registration are applied; labels the
// runner asks for are only honored when the registration left them empty.
func (s *Store) RegisterRunner(ctx context.Context, regToken, name, version, platform string, wantLabels []string, concurrency int) (*store.Runner, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return nil, "", store.Invalid("a runner name (1-64 characters) is required")
	}
	if concurrency < 1 {
		concurrency = 1
	}
	var r *store.Runner
	plain, hash := store.NewToken(store.RunnerTokenPrefix)
	err := s.Tx(ctx, func(tx Querier) error {
		var labels []string
		var orgIDs []uuid.UUID
		err := tx.QueryRow(ctx, `UPDATE runner_registrations SET used_at=$now
			WHERE token_hash=$1 AND used_at IS NULL AND expires_at > $now RETURNING labels, org_ids`,
			store.HashToken(regToken)).Scan(&labels, &orgIDs)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		if len(labels) == 0 {
			labels = store.CleanLabels(wantLabels)
		}
		if orgIDs == nil {
			orgIDs = []uuid.UUID{}
		}
		r, err = scanRunner(tx.QueryRow(ctx, `INSERT INTO runners (name, labels, org_ids, token_hash, max_concurrency, version, platform, last_seen_at,
			id, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$now,$8,$now) RETURNING `+runnerCols, name, labels, orgIDs, hash, concurrency, version, platform, uuid.New()))
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return r, plain, nil
}

// RunnerByToken authenticates a runner and records that it is alive.
func (s *Store) RunnerByToken(ctx context.Context, plain string) (*store.Runner, error) {
	if !strings.HasPrefix(plain, store.RunnerTokenPrefix) {
		return nil, domain.ErrNotFound
	}
	return scanRunner(s.w.QueryRow(ctx, `UPDATE runners SET last_seen_at=$now
		WHERE token_hash=$1 AND is_active RETURNING `+runnerCols, store.HashToken(plain)))
}

func (s *Store) GetRunner(ctx context.Context, id uuid.UUID) (*store.Runner, error) {
	return scanRunner(s.r.QueryRow(ctx, `SELECT `+runnerCols+` FROM runners WHERE id=$1`, id))
}

func (s *Store) ListRunners(ctx context.Context) ([]*store.Runner, error) {
	rows, err := s.r.Query(ctx, `SELECT `+runnerCols+`,
		(SELECT COUNT(*) FROM runner_jobs j WHERE j.runner_id = runners.id AND j.status='running')
		FROM runners ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*store.Runner{}
	for rows.Next() {
		r := &store.Runner{}
		if err := rows.Scan(&r.ID, &r.Name, &r.Labels, &r.OrgIDs, &r.IsActive, &r.MaxConcurrency, &r.Version, &r.Platform,
			&r.LastSeenAt, &r.CreatedAt, &r.RunningJobs); err != nil {
			return nil, err
		}
		fixRunner(r)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpdateRunner(ctx context.Context, id uuid.UUID, p store.RunnerPatch) (*store.Runner, error) {
	r, err := s.GetRunner(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Name != nil {
		n := strings.TrimSpace(*p.Name)
		if n == "" || len(n) > 64 {
			return nil, store.Invalid("a runner name (1-64 characters) is required")
		}
		r.Name = n
	}
	if p.Labels != nil {
		r.Labels = store.CleanLabels(*p.Labels)
	}
	if p.OrgIDs != nil {
		r.OrgIDs = *p.OrgIDs
		if r.OrgIDs == nil {
			r.OrgIDs = []uuid.UUID{}
		}
	}
	if p.IsActive != nil {
		r.IsActive = *p.IsActive
	}
	return scanRunner(s.w.QueryRow(ctx, `UPDATE runners SET name=$2, labels=$3, org_ids=$4, is_active=$5
		WHERE id=$1 RETURNING `+runnerCols, id, r.Name, r.Labels, r.OrgIDs, r.IsActive))
}

// ReportRunner stores the version and concurrency a runner sent with a claim. A blank version or a zero
// concurrency (a runner from before these fields) keeps what is stored; concurrency is clamped to
// 1..MaxRunnerConcurrency. It returns the runner as stored and whether anything changed.
func (s *Store) ReportRunner(ctx context.Context, r *store.Runner, version string, concurrency int) (*store.Runner, bool, error) {
	if len(version) > 32 { // the column's width; a claim must not fail over it
		version = version[:32]
	}
	if concurrency != 0 {
		concurrency = max(1, min(concurrency, store.MaxRunnerConcurrency))
	}
	out, err := scanRunner(s.w.QueryRow(ctx, `UPDATE runners
		SET version = COALESCE(NULLIF($2, ''), version), max_concurrency = COALESCE(NULLIF($3, 0), max_concurrency)
		WHERE id=$1 AND (version <> COALESCE(NULLIF($2, ''), version) OR max_concurrency <> COALESCE(NULLIF($3, 0), max_concurrency))
		RETURNING `+runnerCols, r.ID, version, concurrency))
	if errors.Is(err, domain.ErrNotFound) {
		return r, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// DeleteRunner revokes a runner at once: its token stops working and its running jobs are canceled.
func (s *Store) DeleteRunner(ctx context.Context, id uuid.UUID) error {
	return s.Tx(ctx, func(tx Querier) error {
		if _, err := tx.Exec(ctx, `UPDATE runner_jobs SET status='canceled', finished_at=$now WHERE runner_id=$1 AND status='running'`, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM runners WHERE id=$1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
}

// --- jobs ---

const jobCols = `j.id, j.org_id, j.task_id, j.agent_run_id, j.labels, j.label_match, j.status, j.runner_id, COALESCE(r.name,''), j.lease_until,
	j.payload, j.result, j.created_at, j.started_at, j.finished_at`

const jobFrom = ` FROM runner_jobs j LEFT JOIN runners r ON r.id = j.runner_id`

func scanJob(row Row) (*store.RunnerJob, error) {
	j := &store.RunnerJob{}
	err := row.Scan(&j.ID, &j.OrgID, &j.TaskID, &j.AgentRunID, &j.Labels, &j.LabelMatch, &j.Status, &j.RunnerID, &j.RunnerName, &j.LeaseUntil,
		&j.Payload, &j.Result, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if err != nil {
		return nil, NotFound(err)
	}
	if j.Labels == nil {
		j.Labels = []string{}
	}
	return j, nil
}

func (s *Store) CreateRunnerJob(ctx context.Context, in store.NewRunnerJob) (*store.RunnerJob, error) {
	raw, err := json.Marshal(in.Payload)
	if err != nil {
		return nil, err
	}
	match := in.LabelMatch
	switch match {
	case "":
		match = store.LabelMatchAll
	case store.LabelMatchAll, store.LabelMatchAny:
	default:
		return nil, store.Invalid("label_match must be all or any")
	}
	var id uuid.UUID
	err = s.w.QueryRow(ctx, `INSERT INTO runner_jobs (org_id, task_id, agent_run_id, labels, label_match, payload, id, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$now) RETURNING id`, in.OrgID, in.TaskID, in.AgentRunID, store.CleanLabels(in.Labels), match, string(raw),
		uuid.New()).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.GetRunnerJob(ctx, id)
}

func (s *Store) GetRunnerJob(ctx context.Context, id uuid.UUID) (*store.RunnerJob, error) {
	return scanJob(s.r.QueryRow(ctx, `SELECT `+jobCols+jobFrom+` WHERE j.id=$1`, id))
}

// RunnerJobForRun is the newest runner job of an agent run.
func (s *Store) RunnerJobForRun(ctx context.Context, runID uuid.UUID) (*store.RunnerJob, error) {
	return scanJob(s.r.QueryRow(ctx, `SELECT `+jobCols+jobFrom+` WHERE j.agent_run_id=$1 ORDER BY j.created_at DESC LIMIT 1`, runID))
}

func (s *Store) ListTaskRunnerJobs(ctx context.Context, taskID uuid.UUID) ([]*store.RunnerJob, error) {
	rows, err := s.r.Query(ctx, `SELECT `+jobCols+jobFrom+` WHERE j.task_id=$1 ORDER BY j.created_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*store.RunnerJob{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ClaimRunnerJob hands the oldest queued job the runner may take (its labels cover the job's, or share
// one with them for a label_match 'any' job; a job with no labels fits any runner; the job's org is one
// it serves) and reserves it. It returns nil when there is nothing to do or the
// runner is at its concurrency limit.
func (s *Store) ClaimRunnerJob(ctx context.Context, r *store.Runner) (*store.RunnerJob, error) {
	var id uuid.UUID
	err := s.Tx(ctx, func(tx Querier) error {
		var running int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM runner_jobs WHERE runner_id=$1 AND status='running'`, r.ID).Scan(&running); err != nil {
			return err
		}
		if running >= r.MaxConcurrency {
			return sql.ErrNoRows
		}
		// The job's labels all among the runner's (PostgreSQL's <@), or for 'any' one in common (&&).
		if err := tx.QueryRow(ctx, `SELECT id FROM runner_jobs j
			WHERE status='queued'
			  AND (NOT EXISTS (SELECT 1 FROM json_each(j.labels) l WHERE l.value NOT IN (SELECT value FROM json_each($1)))
			       OR (label_match = 'any' AND EXISTS (SELECT 1 FROM json_each(j.labels) l WHERE l.value IN (SELECT value FROM json_each($1)))))
			  AND (json_array_length($2) = 0 OR org_id IN (SELECT value FROM json_each($2)))
			ORDER BY created_at LIMIT 1`, nonNilLabels(r.Labels), nonNilIDs(r.OrgIDs)).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE runner_jobs SET status='running', runner_id=$2, started_at=$now,
			lease_until=$3 WHERE id=$1`, id, r.ID, time.Now().Add(store.RunnerLease))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.GetRunnerJob(ctx, id)
}

func nonNilLabels(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// HeartbeatRunnerJob extends the lease of a running job owned by the runner and returns the job's
// status, which is "canceled" when the server wants the runner to stop.
func (s *Store) HeartbeatRunnerJob(ctx context.Context, id, runnerID uuid.UUID) (string, error) {
	var status string
	err := s.w.QueryRow(ctx, `UPDATE runner_jobs SET lease_until = CASE WHEN status='running'
			THEN $3 ELSE lease_until END
		WHERE id=$1 AND runner_id=$2 RETURNING status`, id, runnerID, time.Now().Add(store.RunnerLease)).Scan(&status)
	return status, NotFound(err)
}

// CompleteRunnerJob records the outcome of a job the runner owns. It reports false when the job
// was no longer running (canceled or expired meanwhile).
func (s *Store) CompleteRunnerJob(ctx context.Context, id, runnerID uuid.UUID, status string, result any) (bool, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	tag, err := s.w.Exec(ctx, `UPDATE runner_jobs SET status=$3, result=$4, finished_at=$now
		WHERE id=$1 AND runner_id=$2 AND status IN ('running','canceling')`, id, runnerID, status, string(raw))
	return tag.RowsAffected() == 1, err
}

// EndRunnerJob closes a job from the server side (cancel, queue timeout, lost runner) if it is
// still open. It reports whether it changed anything.
func (s *Store) EndRunnerJob(ctx context.Context, id uuid.UUID, status string, result any) (bool, error) {
	var raw []byte
	if result != nil {
		raw, _ = json.Marshal(result)
	}
	tag, err := s.w.Exec(ctx, `UPDATE runner_jobs SET status=$2, result=COALESCE($3, result), finished_at=$now
		WHERE id=$1 AND status IN ('queued','running','canceling')`, id, status, jsonArg(raw))
	return tag.RowsAffected() == 1, err
}

// CancelOrphanRunnerJobs closes jobs left open by a server restart (their agent runs are gone).
func (s *Store) CancelOrphanRunnerJobs(ctx context.Context) error {
	_, err := s.w.Exec(ctx, `UPDATE runner_jobs SET status='canceled', finished_at=$now
		WHERE status IN ('queued','running','canceling')`)
	return err
}

// StopRunnerJob asks the runner to stop: a queued job is simply canceled; a running one becomes
// "canceling" so the runner's next heartbeat stops the agent and it can still report what happened.
func (s *Store) StopRunnerJob(ctx context.Context, id uuid.UUID) error {
	if _, err := s.w.Exec(ctx, `UPDATE runner_jobs SET status='canceled', finished_at=$now WHERE id=$1 AND status='queued'`, id); err != nil {
		return err
	}
	_, err := s.w.Exec(ctx, `UPDATE runner_jobs SET status='canceling' WHERE id=$1 AND status='running'`, id)
	return err
}
