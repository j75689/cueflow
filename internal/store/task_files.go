package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/j75689/cueflow/internal/domain"
)

// TaskFile is a file in a task's working storage. Its bytes live where media bytes do (the shared
// byte layer in blobbed.go), but in their own table: the public /media/{id} route never sees them.
type TaskFile struct {
	ID        uuid.UUID `json:"id"`
	TaskID    uuid.UUID `json:"task_id"`
	Name      string    `json:"name"`
	MimeType  string    `json:"mime_type"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const (
	// MaxTaskFileBytes caps one task file.
	MaxTaskFileBytes = 8 << 20
	maxTaskFileName  = 200
)

// taskFileColumns and scanDest keep every query that loads a TaskFile reading the same columns.
const taskFileColumns = `id, task_id, name, mime_type, size_bytes, created_by, created_at, updated_at`

func (f *TaskFile) scanDest(kind *string) []any {
	return []any{&f.ID, &f.TaskID, &f.Name, &f.MimeType, &f.SizeBytes, &f.CreatedBy, &f.CreatedAt, &f.UpdatedAt, kind}
}

// ValidTaskFileName checks a file name: 1–200 bytes, "/"-separated parts that are neither empty nor
// "." or "..", no backslashes or control characters.
func ValidTaskFileName(name string) error {
	if name == "" || len(name) > maxTaskFileName {
		return Invalid(fmt.Sprintf("a file name must be 1 to %d bytes", maxTaskFileName))
	}
	for _, r := range name {
		if r == '\\' || unicode.IsControl(r) {
			return Invalid("a file name can't contain backslashes or control characters")
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return Invalid(`a file name can't start or end with "/", or contain "//", "." or ".." parts`)
		}
	}
	return nil
}

// WriteTaskFile creates or replaces the task's file name. Writing only ever touches the task's own
// files; a subtask reads its ancestors' files (OpenTaskFile) but can't change them.
func (s *Store) WriteTaskFile(ctx context.Context, taskID uuid.UUID, name, mimeType string, data []byte, createdBy string) (*TaskFile, error) {
	if err := ValidTaskFileName(name); err != nil {
		return nil, err
	}
	if len(data) > MaxTaskFileBytes {
		return nil, Invalid(fmt.Sprintf("a task file can be at most %d MB", MaxTaskFileBytes>>20))
	}
	if mimeType == "" {
		mimeType = "text/plain; charset=utf-8"
	}
	if createdBy == "" {
		createdBy = "agent"
	}
	// Replacing keeps the row's id (and so its blob key), so the new bytes overwrite the old ones.
	var oldKind string
	id := uuid.New()
	err := s.pool.QueryRow(ctx, `SELECT id, storage FROM task_files WHERE task_id=$1 AND name=$2`, taskID, name).Scan(&id, &oldKind)
	if err = NotFound(err); err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	kind, row, err := s.putBytes(ctx, id, data)
	if err != nil {
		return nil, err
	}
	f := &TaskFile{}
	var stored string
	err = s.pool.QueryRow(ctx, `INSERT INTO task_files (id, task_id, name, mime_type, size_bytes, data, storage, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (task_id, name) DO UPDATE SET mime_type=EXCLUDED.mime_type, size_bytes=EXCLUDED.size_bytes,
			data=EXCLUDED.data, storage=EXCLUDED.storage, created_by=EXCLUDED.created_by, updated_at=NOW()
		RETURNING `+taskFileColumns+`, storage`,
		id, taskID, name, mimeType, len(data), row, kind, createdBy).Scan(f.scanDest(&stored)...)
	if err != nil {
		_ = s.dropBytes(ctx, kind, id)
		return nil, err
	}
	if f.ID != id {
		// Another write created the file between our lookup and insert: the row kept its id, so the
		// bytes we just wrote under ours belong to no row. Write them under the row's id instead.
		_ = s.dropBytes(ctx, kind, id)
		if kind, _, err = s.putBytes(ctx, f.ID, data); err != nil {
			return nil, err
		}
	}
	if oldKind != "" && oldKind != kind {
		_ = s.dropBytes(ctx, oldKind, f.ID) // it moved backends (CUEFLOW_MEDIA_STORAGE changed)
	}
	return f, nil
}

// lineage is the task and its ancestors, nearest first, with their depth (0 for the task itself).
const lineageSQL = `WITH RECURSIVE lineage AS (
	SELECT id, parent_id, 0 AS depth FROM tasks WHERE id=$1
	UNION ALL SELECT t.id, t.parent_id, l.depth+1 FROM tasks t JOIN lineage l ON t.id=l.parent_id WHERE l.depth < 20
) `

// ListTaskFiles returns the files taskID can read: its own and its ancestors', by name. A name that
// several of them have resolves to the nearest one, as OpenTaskFile does; TaskID tells whose it is.
func (s *Store) ListTaskFiles(ctx context.Context, taskID uuid.UUID) ([]*TaskFile, error) {
	rows, err := s.pool.Query(ctx, lineageSQL+`SELECT DISTINCT ON (f.name) `+qualify("f", taskFileColumns)+`, f.storage
		FROM task_files f JOIN lineage l ON l.id = f.task_id ORDER BY f.name, l.depth`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TaskFile{}
	for rows.Next() {
		f := &TaskFile{}
		var kind string
		if err := rows.Scan(f.scanDest(&kind)...); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// OpenTaskFile opens the file name as taskID sees it: its own, or else the nearest ancestor's. The
// caller closes the stream; it is seekable, for reading a range.
func (s *Store) OpenTaskFile(ctx context.Context, taskID uuid.UUID, name string) (*TaskFile, io.ReadSeekCloser, error) {
	f := &TaskFile{}
	var kind string
	err := s.pool.QueryRow(ctx, lineageSQL+`SELECT `+qualify("f", taskFileColumns)+`, f.storage
		FROM task_files f JOIN lineage l ON l.id = f.task_id WHERE f.name=$2 ORDER BY l.depth LIMIT 1`,
		taskID, name).Scan(f.scanDest(&kind)...)
	if err != nil {
		return nil, nil, NotFound(err)
	}
	rc, err := s.openBytes(ctx, kind, f.ID, func() ([]byte, error) {
		var data []byte
		err := s.pool.QueryRow(ctx, `SELECT data FROM task_files WHERE id=$1`, f.ID).Scan(&data)
		return data, NotFound(err)
	})
	if err != nil {
		return nil, nil, err
	}
	return f, rc, nil
}

// DeleteTaskFile removes one of the task's own files, in a transaction that commits only once its
// bytes are gone (see DeleteTask).
func (s *Store) DeleteTaskFile(ctx context.Context, taskID uuid.UUID, name string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var f storedBytes
		err := tx.QueryRow(ctx, `DELETE FROM task_files WHERE task_id=$1 AND name=$2 RETURNING id, storage`, taskID, name).Scan(&f.ID, &f.Kind)
		if err != nil {
			return NotFound(err)
		}
		return s.dropAllBytes(ctx, []storedBytes{f})
	})
}

// qualify prefixes every column in a comma-separated list with alias.
func qualify(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}
