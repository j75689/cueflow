package content

import (
	"context"

	"github.com/google/uuid"
)

// --- posts linked to tasks ---

type PostRef struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Status    Status    `json:"status"`
	Platforms []string  `json:"platforms"`
}

func (s *Store) PostsForTask(ctx context.Context, taskID uuid.UUID) ([]PostRef, error) {
	rows, err := s.Pool().Query(ctx, `SELECT id, title, status, platforms FROM posts WHERE task_id=$1 ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PostRef{}
	for rows.Next() {
		var p PostRef
		var st string
		if err := rows.Scan(&p.ID, &p.Title, &st, &p.Platforms); err != nil {
			return nil, err
		}
		p.Status = Status(st)
		out = append(out, p)
	}
	return out, rows.Err()
}

// PublishableCount is how many posts wait to be published (approved, or queued without a lease).
func (s *Store) PublishableCount(ctx context.Context) (int, error) {
	var n int
	err := s.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM posts WHERE status='APPROVED' OR (status='QUEUED' AND lease_until IS NULL)`).Scan(&n)
	return n, err
}
