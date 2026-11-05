package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/store"
)

// ClaimDue is the external clock's heart: it returns the ids of active rows of a clock table
// whose next_run_at has passed and moves each to its following activation, atomically, so that two
// instances ticking at once fire a given activation once.
//
// A row with no next_run_at (new, or its cron/timezone/active flag was just edited) is only
// scheduled, never fired. An activation more than grace overdue (the clock was down) is skipped:
// a missed daily post is better dropped than published at a random hour.
func (s *Store) ClaimDue(ctx context.Context, table string, now time.Time, grace time.Duration, next store.NextFunc) ([]uuid.UUID, error) {
	if !slices.Contains(store.ClockTables, table) {
		return nil, errors.New("store: ClaimDue: unknown table")
	}
	var fired []uuid.UUID
	err := s.Tx(ctx, func(tx Querier) error {
		rows, err := tx.Query(ctx, `SELECT id, cron_expr, timezone, next_run_at FROM `+table+`
			WHERE is_active AND (next_run_at IS NULL OR next_run_at <= $1)`, now)
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
	err := s.Tx(ctx, func(tx Querier) error {
		var scheduled *time.Time
		err := tx.QueryRow(ctx, `SELECT next_run_at FROM clock WHERE name=$1`, name).Scan(&scheduled)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
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
	err := s.r.QueryRow(ctx, `SELECT COUNT(*) FROM tasks t JOIN agents a ON a.id=t.assignee_agent_id AND a.is_active
		LEFT JOIN connectors rc ON rc.id = a.runtime_connector_id
		WHERE t.status='TODO' AND COALESCE(rc.type,'') <> 'external' AND `+claimable).Scan(&n)
	return n, err
}

// --- per-run MCP credentials ---

// CreateRunToken registers a run's MCP credential. The caller puts the returned plain token in the URL.
func (s *Store) CreateRunToken(ctx context.Context, taskID, agentID uuid.UUID, runID *uuid.UUID, ttl time.Duration) (string, error) {
	plain, hash := store.NewToken("cfm_")
	_, err := s.w.Exec(ctx, `INSERT INTO run_tokens (token_hash, task_id, agent_id, run_id, expires_at)
		VALUES ($1,$2,$3,$4,$5)`, hash, taskID, agentID, runID, time.Now().Add(ttl))
	return plain, err
}

// RunTokenByPlain resolves a token from an MCP URL; it is ErrNotFound once expired or deleted.
func (s *Store) RunTokenByPlain(ctx context.Context, plain string) (*store.RunToken, error) {
	t := &store.RunToken{}
	err := s.r.QueryRow(ctx, `SELECT task_id, agent_id, run_id, outcome FROM run_tokens
		WHERE token_hash=$1 AND expires_at > $now`, store.HashToken(plain)).Scan(&t.TaskID, &t.AgentID, &t.RunID, &t.Outcome)
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
	_, err = s.w.Exec(ctx, `UPDATE run_tokens SET outcome=$2 WHERE token_hash=$1 AND outcome IS NULL`, store.HashToken(plain), string(raw))
	return err
}

// DeleteRunToken ends a run's credential.
func (s *Store) DeleteRunToken(ctx context.Context, plain string) error {
	_, err := s.w.Exec(ctx, `DELETE FROM run_tokens WHERE token_hash=$1`, store.HashToken(plain))
	return err
}

// PurgeRunTokens removes expired credentials (called from the clock).
func (s *Store) PurgeRunTokens(ctx context.Context) error {
	_, err := s.w.Exec(ctx, `DELETE FROM run_tokens WHERE expires_at < $now`)
	return err
}

// FailAbandoned is the multi-instance counterpart of the start-up cleanups: it closes work whose
// owner is evidently gone, judged by leases and ages instead of "everything running is stale".
// An agent run whose task is no longer in progress and a runner job whose lease lapsed are both
// unowned. Modules close their own work from their sweeps.
func (s *Store) FailAbandoned(ctx context.Context) error {
	now := time.Now()
	stmts := []struct {
		q     string
		since time.Time
	}{
		{`UPDATE agent_runs AS r SET status='FAILED', error='interrupted (the worker stopped)', finished_at=$now
			FROM tasks t WHERE r.task_id=t.id AND r.status='RUNNING' AND r.runtime <> 'external'
			AND t.status <> 'IN_PROGRESS' AND r.started_at < $1`, now.Add(-2 * time.Minute)},
		{`UPDATE runner_jobs SET status='expired', finished_at=$now
			WHERE status IN ('running','canceling') AND lease_until < $1`, now.Add(-time.Minute)},
		{`UPDATE runner_jobs SET status='expired', finished_at=$now
			WHERE status='queued' AND created_at < $1`, now.Add(-24 * time.Hour)},
	}
	for _, st := range stmts {
		if _, err := s.w.Exec(ctx, st.q, st.since); err != nil {
			return err
		}
	}
	return nil
}
