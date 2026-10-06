package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/j75689/cueflow/internal/domain"
)

// Token prefixes: a registration token is traded once for a runner token.
const (
	RegistrationTokenPrefix = "cfreg_"
	RunnerTokenPrefix       = "cfr_"
)

// RunnerOnlineWindow is how recently a runner must have polled to count as online.
const RunnerOnlineWindow = 90 * time.Second

// RunnerLease is how long a claimed job stays reserved without a heartbeat.
const RunnerLease = 2 * time.Minute

// Job states.
const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
	// JobCanceling: the server wants the runner to stop; the runner still reports its last events
	// and completes the job.
	JobCanceling = "canceling"
	JobCanceled  = "canceled"
	JobExpired   = "expired"
)

// JobFinished reports whether a runner job is over.
func JobFinished(status string) bool {
	return status != JobQueued && status != JobRunning && status != JobCanceling
}

// NewToken returns a random token with prefix and its SHA-256 (the only thing stored).
func NewToken(prefix string) (plain string, hash []byte) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	plain = prefix + hex.EncodeToString(b)
	return plain, HashToken(plain)
}

func HashToken(plain string) []byte {
	h := sha256.Sum256([]byte(plain))
	return h[:]
}

type Runner struct {
	ID             uuid.UUID   `json:"id"`
	Name           string      `json:"name"`
	Labels         []string    `json:"labels"`
	OrgIDs         []uuid.UUID `json:"org_ids"`
	IsActive       bool        `json:"is_active"`
	MaxConcurrency int         `json:"max_concurrency"`
	Version        string      `json:"version"`
	Platform       string      `json:"platform"`
	LastSeenAt     *time.Time  `json:"last_seen_at"`
	CreatedAt      time.Time   `json:"created_at"`
	// Online and RunningJobs are filled by ListRunners.
	Online      bool `json:"online"`
	RunningJobs int  `json:"running_jobs"`
}

const runnerCols = `id, name, labels, org_ids, is_active, max_concurrency, version, platform, last_seen_at, created_at`

func (r *Runner) fix() {
	if r.Labels == nil {
		r.Labels = []string{}
	}
	if r.OrgIDs == nil {
		r.OrgIDs = []uuid.UUID{}
	}
	r.Online = r.IsActive && r.LastSeenAt != nil && time.Since(*r.LastSeenAt) < RunnerOnlineWindow
}

func scanRunner(row pgx.Row) (*Runner, error) {
	r := &Runner{}
	err := row.Scan(&r.ID, &r.Name, &r.Labels, &r.OrgIDs, &r.IsActive, &r.MaxConcurrency, &r.Version, &r.Platform,
		&r.LastSeenAt, &r.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	r.fix()
	return r, nil
}

func runnerLabels(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, l := range in {
		l = strings.ToLower(strings.TrimSpace(l))
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// CleanLabels normalizes runner labels the way the store keeps them.
func CleanLabels(in []string) []string { return runnerLabels(in) }

// CreateRegistration makes a one-time token a new runner trades for its own credential.
func (s *Store) CreateRegistration(ctx context.Context, labels []string, orgIDs []uuid.UUID, ttl time.Duration) (string, time.Time, error) {
	plain, hash := NewToken(RegistrationTokenPrefix)
	if orgIDs == nil {
		orgIDs = []uuid.UUID{}
	}
	exp := time.Now().Add(ttl)
	_, err := s.pool.Exec(ctx, `INSERT INTO runner_registrations (token_hash, labels, org_ids, expires_at) VALUES ($1,$2,$3,$4)`,
		hash, runnerLabels(labels), orgIDs, exp)
	return plain, exp, err
}

// RegisterRunner consumes a registration token and creates the runner. It returns the runner's
// token (shown once). The labels and orgs the admin chose at registration are applied; labels the
// runner asks for are only honored when the registration left them empty.
func (s *Store) RegisterRunner(ctx context.Context, regToken, name, version, platform string, wantLabels []string, concurrency int) (*Runner, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return nil, "", invalid("a runner name (1-64 characters) is required")
	}
	if concurrency < 1 {
		concurrency = 1
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)
	var labels []string
	var orgIDs []uuid.UUID
	err = tx.QueryRow(ctx, `UPDATE runner_registrations SET used_at=NOW()
		WHERE token_hash=$1 AND used_at IS NULL AND expires_at > NOW() RETURNING labels, org_ids`,
		HashToken(regToken)).Scan(&labels, &orgIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", domain.ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if len(labels) == 0 {
		labels = runnerLabels(wantLabels)
	}
	if orgIDs == nil {
		orgIDs = []uuid.UUID{}
	}
	plain, hash := NewToken(RunnerTokenPrefix)
	r, err := scanRunner(tx.QueryRow(ctx, `INSERT INTO runners (name, labels, org_ids, token_hash, max_concurrency, version, platform, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NOW()) RETURNING `+runnerCols, name, labels, orgIDs, hash, concurrency, version, platform))
	if err != nil {
		return nil, "", err
	}
	return r, plain, tx.Commit(ctx)
}

// RunnerByToken authenticates a runner and records that it is alive.
func (s *Store) RunnerByToken(ctx context.Context, plain string) (*Runner, error) {
	if !strings.HasPrefix(plain, RunnerTokenPrefix) {
		return nil, domain.ErrNotFound
	}
	return scanRunner(s.pool.QueryRow(ctx, `UPDATE runners SET last_seen_at=NOW()
		WHERE token_hash=$1 AND is_active RETURNING `+runnerCols, HashToken(plain)))
}

func (s *Store) GetRunner(ctx context.Context, id uuid.UUID) (*Runner, error) {
	return scanRunner(s.pool.QueryRow(ctx, `SELECT `+runnerCols+` FROM runners WHERE id=$1`, id))
}

func (s *Store) ListRunners(ctx context.Context) ([]*Runner, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+runnerCols+`,
		(SELECT COUNT(*) FROM runner_jobs j WHERE j.runner_id = runners.id AND j.status='running')
		FROM runners ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Runner{}
	for rows.Next() {
		r := &Runner{}
		if err := rows.Scan(&r.ID, &r.Name, &r.Labels, &r.OrgIDs, &r.IsActive, &r.MaxConcurrency, &r.Version, &r.Platform,
			&r.LastSeenAt, &r.CreatedAt, &r.RunningJobs); err != nil {
			return nil, err
		}
		r.fix()
		out = append(out, r)
	}
	return out, rows.Err()
}

type RunnerPatch struct {
	Name           *string
	Labels         *[]string
	OrgIDs         *[]uuid.UUID
	IsActive       *bool
	MaxConcurrency *int
}

func (s *Store) UpdateRunner(ctx context.Context, id uuid.UUID, p RunnerPatch) (*Runner, error) {
	r, err := s.GetRunner(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Name != nil {
		n := strings.TrimSpace(*p.Name)
		if n == "" || len(n) > 64 {
			return nil, invalid("a runner name (1-64 characters) is required")
		}
		r.Name = n
	}
	if p.Labels != nil {
		r.Labels = runnerLabels(*p.Labels)
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
	if p.MaxConcurrency != nil {
		if *p.MaxConcurrency < 1 || *p.MaxConcurrency > 16 {
			return nil, invalid("max concurrency must be between 1 and 16")
		}
		r.MaxConcurrency = *p.MaxConcurrency
	}
	return scanRunner(s.pool.QueryRow(ctx, `UPDATE runners SET name=$2, labels=$3, org_ids=$4, is_active=$5, max_concurrency=$6
		WHERE id=$1 RETURNING `+runnerCols, id, r.Name, r.Labels, r.OrgIDs, r.IsActive, r.MaxConcurrency))
}

// DeleteRunner revokes a runner at once: its token stops working and its running jobs are canceled.
func (s *Store) DeleteRunner(ctx context.Context, id uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE runner_jobs SET status='canceled', finished_at=NOW() WHERE runner_id=$1 AND status='running'`, id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM runners WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return tx.Commit(ctx)
}

// --- jobs ---

type RunnerJob struct {
	ID         uuid.UUID       `json:"id"`
	OrgID      uuid.UUID       `json:"org_id"`
	TaskID     uuid.UUID       `json:"task_id"`
	AgentRunID *uuid.UUID      `json:"agent_run_id"`
	Labels     []string        `json:"labels"`
	Status     string          `json:"status"`
	RunnerID   *uuid.UUID      `json:"runner_id"`
	RunnerName string          `json:"runner_name"`
	LeaseUntil *time.Time      `json:"lease_until"`
	Payload    json.RawMessage `json:"-"`
	Result     json.RawMessage `json:"result"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
}

const jobCols = `j.id, j.org_id, j.task_id, j.agent_run_id, j.labels, j.status, j.runner_id, COALESCE(r.name,''), j.lease_until,
	j.payload, j.result, j.created_at, j.started_at, j.finished_at`

const jobFrom = ` FROM runner_jobs j LEFT JOIN runners r ON r.id = j.runner_id`

func scanJob(row pgx.Row) (*RunnerJob, error) {
	j := &RunnerJob{}
	err := row.Scan(&j.ID, &j.OrgID, &j.TaskID, &j.AgentRunID, &j.Labels, &j.Status, &j.RunnerID, &j.RunnerName, &j.LeaseUntil,
		&j.Payload, &j.Result, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if err != nil {
		return nil, notFound(err)
	}
	if j.Labels == nil {
		j.Labels = []string{}
	}
	return j, nil
}

type NewRunnerJob struct {
	OrgID      uuid.UUID
	TaskID     uuid.UUID
	AgentRunID *uuid.UUID
	Labels     []string
	Payload    any
}

func (s *Store) CreateRunnerJob(ctx context.Context, in NewRunnerJob) (*RunnerJob, error) {
	raw, err := json.Marshal(in.Payload)
	if err != nil {
		return nil, err
	}
	var id uuid.UUID
	err = s.pool.QueryRow(ctx, `INSERT INTO runner_jobs (org_id, task_id, agent_run_id, labels, payload)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`, in.OrgID, in.TaskID, in.AgentRunID, runnerLabels(in.Labels), raw).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.GetRunnerJob(ctx, id)
}

func (s *Store) GetRunnerJob(ctx context.Context, id uuid.UUID) (*RunnerJob, error) {
	return scanJob(s.pool.QueryRow(ctx, `SELECT `+jobCols+jobFrom+` WHERE j.id=$1`, id))
}

func (s *Store) ListTaskRunnerJobs(ctx context.Context, taskID uuid.UUID) ([]*RunnerJob, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+jobCols+jobFrom+` WHERE j.task_id=$1 ORDER BY j.created_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*RunnerJob{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ClaimRunnerJob hands the oldest queued job the runner may take (its labels cover the job's, the
// job's org is one it serves) and reserves it. It returns nil when there is nothing to do or the
// runner is at its concurrency limit.
func (s *Store) ClaimRunnerJob(ctx context.Context, r *Runner) (*RunnerJob, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var running int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM runner_jobs WHERE runner_id=$1 AND status='running'`, r.ID).Scan(&running); err != nil {
		return nil, err
	}
	if running >= r.MaxConcurrency {
		return nil, nil
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM runner_jobs
		WHERE status='queued' AND labels <@ $1 AND (cardinality($2::uuid[]) = 0 OR org_id = ANY($2))
		ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`, r.Labels, r.OrgIDs).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE runner_jobs SET status='running', runner_id=$2, started_at=NOW(),
		lease_until=NOW() + make_interval(secs => $3) WHERE id=$1`, id, r.ID, RunnerLease.Seconds()); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetRunnerJob(ctx, id)
}

// HeartbeatRunnerJob extends the lease of a running job owned by the runner and returns the job's
// status, which is "canceled" when the server wants the runner to stop.
func (s *Store) HeartbeatRunnerJob(ctx context.Context, id, runnerID uuid.UUID) (string, error) {
	var status string
	err := s.pool.QueryRow(ctx, `UPDATE runner_jobs SET lease_until = CASE WHEN status='running'
			THEN NOW() + make_interval(secs => $3) ELSE lease_until END
		WHERE id=$1 AND runner_id=$2 RETURNING status`, id, runnerID, RunnerLease.Seconds()).Scan(&status)
	return status, notFound(err)
}

// CompleteRunnerJob records the outcome of a job the runner owns. It reports false when the job
// was no longer running (canceled or expired meanwhile).
func (s *Store) CompleteRunnerJob(ctx context.Context, id, runnerID uuid.UUID, status string, result any) (bool, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE runner_jobs SET status=$3, result=$4, finished_at=NOW()
		WHERE id=$1 AND runner_id=$2 AND status IN ('running','canceling')`, id, runnerID, status, raw)
	return tag.RowsAffected() == 1, err
}

// EndRunnerJob closes a job from the server side (cancel, queue timeout, lost runner) if it is
// still open. It reports whether it changed anything.
func (s *Store) EndRunnerJob(ctx context.Context, id uuid.UUID, status string, result any) (bool, error) {
	var raw []byte
	if result != nil {
		raw, _ = json.Marshal(result)
	}
	tag, err := s.pool.Exec(ctx, `UPDATE runner_jobs SET status=$2, result=COALESCE($3, result), finished_at=NOW()
		WHERE id=$1 AND status IN ('queued','running','canceling')`, id, status, raw)
	return tag.RowsAffected() == 1, err
}

// CancelOrphanRunnerJobs closes jobs left open by a server restart (their agent runs are gone).
func (s *Store) CancelOrphanRunnerJobs(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE runner_jobs SET status='canceled', finished_at=NOW()
		WHERE status IN ('queued','running','canceling')`)
	return err
}

// StopRunnerJob asks the runner to stop: a queued job is simply canceled; a running one becomes
// "canceling" so the runner's next heartbeat stops the agent and it can still report what happened.
func (s *Store) StopRunnerJob(ctx context.Context, id uuid.UUID) error {
	if _, err := s.pool.Exec(ctx, `UPDATE runner_jobs SET status='canceled', finished_at=NOW() WHERE id=$1 AND status='queued'`, id); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE runner_jobs SET status='canceling' WHERE id=$1 AND status='running'`, id)
	return err
}
