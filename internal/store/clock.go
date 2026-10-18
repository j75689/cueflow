package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// NextFunc computes the next activation of a cron expression after t.
type NextFunc func(expr, tz string, t time.Time) (time.Time, error)

// ClockTables are the tables ClaimDue may read: rows with cron_expr, timezone, is_active and
// next_run_at columns. Modules append theirs (the content module adds "pipelines").
var ClockTables = []string{"schedules"}

// ClaimDue is the external clock's heart: it returns the ids of active schedules or pipelines
// whose next_run_at has passed and moves each to its following activation, atomically, so that two
// instances ticking at once fire a given activation once.
//
// A row with no next_run_at (new, or its cron/timezone/active flag was just edited) is only
// scheduled, never fired. An activation more than grace overdue (the clock was down) is skipped:
// a missed daily post is better dropped than published at a random hour.
func (s *Store) ClaimDue(ctx context.Context, table string, now time.Time, grace time.Duration, next NextFunc) ([]uuid.UUID, error) {
	if !slices.Contains(ClockTables, table) {
		return nil, errors.New("store: ClaimDue: unknown table")
	}
	var fired []uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, cron_expr, timezone, next_run_at FROM `+table+`
			WHERE is_active AND (next_run_at IS NULL OR next_run_at <= $1) FOR UPDATE SKIP LOCKED`, now)
		if err != nil {
			return err
		}
		type due struct {
			id        uuid.UUID
			expr, tz  string
			scheduled *time.Time
		}
		var list []due
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.id, &d.expr, &d.tz, &d.scheduled); err != nil {
				rows.Close()
				return err
			}
			list = append(list, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, d := range list {
			n, err := next(d.expr, d.tz, now)
			if err != nil || n.IsZero() {
				continue // a bad expression never fires; the editor already validated new ones
			}
			if _, err := tx.Exec(ctx, `UPDATE `+table+` SET next_run_at=$2 WHERE id=$1`, d.id, n); err != nil {
				return err
			}
			if d.scheduled != nil && now.Sub(*d.scheduled) <= grace {
				fired = append(fired, d.id)
			}
		}
		return nil
	})
	return fired, err
}

// ClaimClock does the same for a named singleton (the review digest): it reports whether the
// activation is due now, and moves it on. The first call only schedules it.
func (s *Store) ClaimClock(ctx context.Context, name string, now time.Time, grace time.Duration, next func(time.Time) (time.Time, error)) (bool, error) {
	fire := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var scheduled *time.Time
		err := tx.QueryRow(ctx, `SELECT next_run_at FROM clock WHERE name=$1 FOR UPDATE`, name).Scan(&scheduled)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if scheduled != nil && scheduled.After(now) {
			return nil
		}
		n, err := next(now)
		if err != nil || n.IsZero() {
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO clock (name, next_run_at) VALUES ($1,$2)
			ON CONFLICT (name) DO UPDATE SET next_run_at=EXCLUDED.next_run_at`, name, n); err != nil {
			return err
		}
		fire = scheduled != nil && now.Sub(*scheduled) <= grace
		return nil
	})
	return fire, err
}

// TodoTaskCount is how many TODO tasks the server's own agent loop could claim now.
func (s *Store) TodoTaskCount(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM tasks t JOIN agents a ON a.id=t.assignee_agent_id AND a.is_active
		LEFT JOIN connectors rc ON rc.id = a.runtime_connector_id
		WHERE t.status='TODO' AND COALESCE(rc.type,'') <> 'external'`).Scan(&n)
	return n, err
}

// --- per-run MCP credentials ---

// RunToken is the database record behind a per-run MCP URL.
type RunToken struct {
	TaskID, AgentID uuid.UUID
	// Outcome is what a terminal tool call decided ({"kind","text"}); nil while the run is open.
	Outcome json.RawMessage
}

// CreateRunToken registers a run's MCP credential. The caller puts the returned plain token in the URL.
func (s *Store) CreateRunToken(ctx context.Context, taskID, agentID uuid.UUID, ttl time.Duration) (string, error) {
	plain, hash := NewToken("cfm_")
	_, err := s.pool.Exec(ctx, `INSERT INTO run_tokens (token_hash, task_id, agent_id, expires_at)
		VALUES ($1,$2,$3, NOW() + make_interval(secs => $4))`, hash, taskID, agentID, ttl.Seconds())
	return plain, err
}

// RunTokenByPlain resolves a token from an MCP URL; it is ErrNotFound once expired or deleted.
func (s *Store) RunTokenByPlain(ctx context.Context, plain string) (*RunToken, error) {
	t := &RunToken{}
	err := s.pool.QueryRow(ctx, `SELECT task_id, agent_id, outcome FROM run_tokens
		WHERE token_hash=$1 AND expires_at > NOW()`, HashToken(plain)).Scan(&t.TaskID, &t.AgentID, &t.Outcome)
	if err != nil {
		return nil, NotFound(err)
	}
	return t, nil
}

// SetRunTokenOutcome records a terminal tool call's outcome (the first one wins).
func (s *Store) SetRunTokenOutcome(ctx context.Context, plain string, outcome any) error {
	raw, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE run_tokens SET outcome=$2 WHERE token_hash=$1 AND outcome IS NULL`, HashToken(plain), raw)
	return err
}

// DeleteRunToken ends a run's credential.
func (s *Store) DeleteRunToken(ctx context.Context, plain string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM run_tokens WHERE token_hash=$1`, HashToken(plain))
	return err
}

// PurgeRunTokens removes expired credentials (called from the clock).
func (s *Store) PurgeRunTokens(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM run_tokens WHERE expires_at < NOW()`)
	return err
}

// FailAbandoned is the multi-instance counterpart of the start-up cleanups: it closes work whose
// owner is evidently gone, judged by leases and ages instead of "everything running is stale".
// A pipeline run past its 30-minute cap, an agent run whose task is no longer in progress, and a
// runner job whose lease lapsed are all unowned.
func (s *Store) FailAbandoned(ctx context.Context) error {
	stmts := []string{
		`UPDATE pipeline_runs SET status='FAILED', error='interrupted (the worker stopped)', finished_at=NOW()
			WHERE status='RUNNING' AND started_at < NOW() - INTERVAL '45 minutes'`,
		`UPDATE agent_runs r SET status='FAILED', error='interrupted (the worker stopped)', finished_at=NOW()
			FROM tasks t WHERE r.task_id=t.id AND r.status='RUNNING' AND r.runtime <> 'external'
			AND t.status <> 'IN_PROGRESS' AND r.started_at < NOW() - INTERVAL '2 minutes'`,
		`UPDATE runner_jobs SET status='expired', finished_at=NOW()
			WHERE status IN ('running','canceling') AND lease_until < NOW() - INTERVAL '1 minute'`,
		`UPDATE runner_jobs SET status='expired', finished_at=NOW()
			WHERE status='queued' AND created_at < NOW() - INTERVAL '24 hours'`,
	}
	for _, q := range stmts {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
