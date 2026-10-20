package store

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/domain"
)

// AgentTokenPrefix marks credentials that act as an agent over MCP.
const AgentTokenPrefix = "cfa_"

// ExternalRuntimeType is the runtime connector type for agents that drive their own work: the
// server never starts them; they claim tasks over MCP.
const ExternalRuntimeType = "external"

type AgentToken struct {
	ID         uuid.UUID  `json:"id"`
	AgentID    uuid.UUID  `json:"agent_id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// CreateAgentToken returns the new credential in plain text, once.
func (s *Store) CreateAgentToken(ctx context.Context, agentID uuid.UUID, name string) (*AgentToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return nil, "", Invalid("a token name (1-64 characters) is required")
	}
	if _, err := s.GetAgent(ctx, agentID); err != nil {
		return nil, "", err
	}
	plain, hash := NewToken(AgentTokenPrefix)
	t := &AgentToken{AgentID: agentID, Name: name}
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_tokens (agent_id, name, token_hash) VALUES ($1,$2,$3)
		RETURNING id, created_at`, agentID, name, hash).Scan(&t.ID, &t.CreatedAt)
	return t, plain, err
}

func (s *Store) ListAgentTokens(ctx context.Context, agentID uuid.UUID) ([]*AgentToken, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, agent_id, name, created_at, last_used_at FROM agent_tokens
		WHERE agent_id=$1 ORDER BY created_at`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AgentToken{}
	for rows.Next() {
		t := &AgentToken{}
		if err := rows.Scan(&t.ID, &t.AgentID, &t.Name, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAgentToken makes a token stop working at once.
func (s *Store) RevokeAgentToken(ctx context.Context, agentID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM agent_tokens WHERE id=$1 AND agent_id=$2`, id, agentID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// AgentByToken resolves a cfa_ credential to its (active) agent.
func (s *Store) AgentByToken(ctx context.Context, plain string) (*Agent, error) {
	if !strings.HasPrefix(plain, AgentTokenPrefix) {
		return nil, domain.ErrNotFound
	}
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `UPDATE agent_tokens t SET last_used_at=NOW() FROM agents a
		WHERE t.token_hash=$1 AND a.id=t.agent_id AND a.is_active RETURNING t.agent_id`, HashToken(plain)).Scan(&id)
	if err != nil {
		return nil, NotFound(err)
	}
	return s.GetAgent(ctx, id)
}

// ClaimTaskForAgent moves a TODO task assigned to the agent to IN_PROGRESS for a self-driving
// agent, reserving it for lease. taskID nil takes the agent's most urgent TODO task. It returns
// nil when there is nothing to claim.
func (s *Store) ClaimTaskForAgent(ctx context.Context, agentID uuid.UUID, taskID *uuid.UUID, lease time.Duration) (*Task, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `UPDATE tasks SET status='IN_PROGRESS', lease_until = NOW() + make_interval(secs => $3),
		attempts = attempts + 1, started_at = COALESCE(started_at, NOW()), updated_at = NOW(), version = version + 1
		WHERE id = (
			SELECT t.id FROM tasks t WHERE t.status='TODO' AND t.assignee_agent_id=$1 AND ($2::uuid IS NULL OR t.id=$2)
			ORDER BY CASE t.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END, t.updated_at
			LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id`, agentID, taskID, lease.Seconds()).Scan(&id)
	if err != nil {
		if NotFound(err) == domain.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	return s.GetTask(ctx, id)
}

// RunningAgentRun is the open run of a task for an agent, if any.
func (s *Store) RunningAgentRun(ctx context.Context, taskID, agentID uuid.UUID) (*AgentRun, error) {
	var r AgentRun
	err := s.pool.QueryRow(ctx, `SELECT `+agentRunColumns+` FROM agent_runs
		WHERE task_id=$1 AND agent_id=$2 AND status='RUNNING' ORDER BY started_at DESC LIMIT 1`, taskID, agentID).
		Scan(r.scanDest()...)
	if err != nil {
		return nil, NotFound(err)
	}
	return &r, nil
}

// FailExternalRuns closes the open runs of a self-driven task whose lease ran out.
func (s *Store) FailExternalRuns(ctx context.Context, taskID uuid.UUID, reason string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_runs SET status='FAILED', error=$2, finished_at=NOW()
		WHERE task_id=$1 AND status='RUNNING' AND runtime='external'`, taskID, reason)
	return err
}
