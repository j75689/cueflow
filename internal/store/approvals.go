package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/j75689/cueflow/internal/domain"
)

// ToolApproval is an agent's call to an "ask" tool, waiting for or decided by a human.
type ToolApproval struct {
	ID          uuid.UUID       `json:"id"`
	TaskID      uuid.UUID       `json:"task_id"`
	AgentID     *uuid.UUID      `json:"agent_id"`
	ConnectorID *uuid.UUID      `json:"connector_id"`
	ServerName  string          `json:"server_name"`
	Tool        string          `json:"tool"`
	Arguments   json.RawMessage `json:"arguments"`
	Status      string          `json:"status"` // pending | approved | rejected
	Result      string          `json:"result"`
	IsError     bool            `json:"is_error"`
	Note        string          `json:"note"`
	CreatedAt   time.Time       `json:"created_at"`
	DecidedAt   *time.Time      `json:"decided_at"`
}

const approvalColumns = `id, task_id, agent_id, connector_id, server_name, tool, arguments, status, result, is_error,
	note, created_at, decided_at`

func scanApproval(row pgx.Row) (*ToolApproval, error) {
	var a ToolApproval
	if err := row.Scan(&a.ID, &a.TaskID, &a.AgentID, &a.ConnectorID, &a.ServerName, &a.Tool, &a.Arguments, &a.Status,
		&a.Result, &a.IsError, &a.Note, &a.CreatedAt, &a.DecidedAt); err != nil {
		return nil, NotFound(err)
	}
	return &a, nil
}

func (s *Store) CreateToolApproval(ctx context.Context, a *ToolApproval) (*ToolApproval, error) {
	if len(a.Arguments) == 0 {
		a.Arguments = json.RawMessage(`{}`)
	}
	return scanApproval(s.pool.QueryRow(ctx, `INSERT INTO tool_approvals (task_id, agent_id, connector_id, server_name, tool, arguments)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING `+approvalColumns,
		a.TaskID, a.AgentID, a.ConnectorID, a.ServerName, a.Tool, a.Arguments))
}

func (s *Store) GetToolApproval(ctx context.Context, id uuid.UUID) (*ToolApproval, error) {
	return scanApproval(s.pool.QueryRow(ctx, `SELECT `+approvalColumns+` FROM tool_approvals WHERE id=$1`, id))
}

func (s *Store) ListToolApprovals(ctx context.Context, taskID uuid.UUID) ([]*ToolApproval, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+approvalColumns+` FROM tool_approvals WHERE task_id=$1 ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ToolApproval{}
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ClaimToolApproval moves a pending approval to approved or rejected; false if it was already decided.
func (s *Store) ClaimToolApproval(ctx context.Context, id uuid.UUID, status, note string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE tool_approvals SET status=$2, note=$3, decided_at=NOW()
		WHERE id=$1 AND status='pending'`, id, status, note)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SetToolApprovalResult records what an approved call returned.
func (s *Store) SetToolApprovalResult(ctx context.Context, id uuid.UUID, result string, isError bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE tool_approvals SET result=$2, is_error=$3 WHERE id=$1`, id, result, isError)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
