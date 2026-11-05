package sqlite

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

const approvalColumns = `id, task_id, agent_id, connector_id, server_name, tool, arguments, status, result, is_error,
	note, created_at, decided_at, called_as, result_file`

func scanApproval(row Row) (*store.ToolApproval, error) {
	var a store.ToolApproval
	if err := row.Scan(&a.ID, &a.TaskID, &a.AgentID, &a.ConnectorID, &a.ServerName, &a.Tool, &a.Arguments, &a.Status,
		&a.Result, &a.IsError, &a.Note, &a.CreatedAt, &a.DecidedAt, &a.CalledAs, &a.ResultFile); err != nil {
		return nil, NotFound(err)
	}
	return &a, nil
}

func (s *Store) CreateToolApproval(ctx context.Context, a *store.ToolApproval) (*store.ToolApproval, error) {
	if len(a.Arguments) == 0 {
		a.Arguments = json.RawMessage(`{}`)
	}
	return scanApproval(s.w.QueryRow(ctx, `INSERT INTO tool_approvals (task_id, agent_id, connector_id, server_name, tool, arguments, called_as,
		id, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$now) RETURNING `+approvalColumns,
		a.TaskID, a.AgentID, a.ConnectorID, a.ServerName, a.Tool, a.Arguments, a.CalledAs, uuid.New()))
}

func (s *Store) GetToolApproval(ctx context.Context, id uuid.UUID) (*store.ToolApproval, error) {
	return scanApproval(s.r.QueryRow(ctx, `SELECT `+approvalColumns+` FROM tool_approvals WHERE id=$1`, id))
}

func (s *Store) ListToolApprovals(ctx context.Context, taskID uuid.UUID) ([]*store.ToolApproval, error) {
	rows, err := s.r.Query(ctx, `SELECT `+approvalColumns+` FROM tool_approvals WHERE task_id=$1 ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*store.ToolApproval{}
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
	tag, err := s.w.Exec(ctx, `UPDATE tool_approvals SET status=$2, note=$3, decided_at=$now
		WHERE id=$1 AND status='pending'`, id, status, note)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SetToolApprovalResult records what an approved call returned.
// SetToolApprovalResult records what an approved call returned; file is the task file a long result
// was saved to, or "".
func (s *Store) SetToolApprovalResult(ctx context.Context, id uuid.UUID, result string, isError bool, file string) error {
	tag, err := s.w.Exec(ctx, `UPDATE tool_approvals SET result=$2, is_error=$3, result_file=$4 WHERE id=$1`, id, result, isError, file)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
