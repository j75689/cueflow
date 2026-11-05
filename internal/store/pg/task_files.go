package pg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/j75689/cueflow/internal/blob"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

// taskFileColumns and taskFileDest keep every query that loads a TaskFile reading the same columns.
const taskFileColumns = `id, task_id, name, mime_type, size_bytes, created_by, created_at, updated_at`

func taskFileDest(f *store.TaskFile, kind *string) []any {
	return []any{&f.ID, &f.TaskID, &f.Name, &f.MimeType, &f.SizeBytes, &f.CreatedBy, &f.CreatedAt, &f.UpdatedAt, kind}
}

// maxShortField is the size of the mime_type and created_by columns.
const maxShortField = 64

// WriteTaskFile creates or replaces the task's file name. Writing only ever touches the task's own
// files; a subtask reads its ancestors' files (OpenTaskFile) but can't change them.
//
// Every write stores its bytes under a fresh row id and the upsert swaps that id in, so a reader
// sees either the old file or the new one, never new bytes with old metadata, and a failed write
// leaves the old file untouched. The old bytes are deleted after the commit. A file's id therefore
// changes when it is replaced; refer to files by task and name.
func (s *Store) WriteTaskFile(ctx context.Context, taskID uuid.UUID, name, mimeType string, data []byte, createdBy string) (*store.TaskFile, error) {
	return s.writeTaskFile(ctx, taskID, name, mimeType, data, createdBy, true)
}

// CreateTaskFile is WriteTaskFile that never replaces: if the task has a file of that name it
// returns ErrTaskFileExists and writes nothing.
func (s *Store) CreateTaskFile(ctx context.Context, taskID uuid.UUID, name, mimeType string, data []byte, createdBy string) (*store.TaskFile, error) {
	return s.writeTaskFile(ctx, taskID, name, mimeType, data, createdBy, false)
}

// CountTaskFiles counts the task's own files whose names start with prefix.
func (s *Store) CountTaskFiles(ctx context.Context, taskID uuid.UUID, prefix string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM task_files WHERE task_id=$1 AND starts_with(name, $2)`, taskID, prefix).Scan(&n)
	return n, err
}

func (s *Store) writeTaskFile(ctx context.Context, taskID uuid.UUID, name, mimeType string, data []byte, createdBy string, replace bool) (*store.TaskFile, error) {
	if err := store.ValidTaskFileName(name); err != nil {
		return nil, err
	}
	if len(data) > store.MaxTaskFileBytes {
		return nil, store.Invalid(fmt.Sprintf("a task file can be at most %d MB", store.MaxTaskFileBytes>>20))
	}
	if mimeType == "" {
		mimeType = "text/plain; charset=utf-8"
	}
	if createdBy == "" {
		createdBy = "agent"
	}
	for field, v := range map[string]string{"MIME type": mimeType, "author": createdBy} {
		if !utf8.ValidString(v) || utf8.RuneCountInString(v) > maxShortField {
			return nil, store.Invalid(fmt.Sprintf("a file's %s must be valid UTF-8 of at most %d characters", field, maxShortField))
		}
	}
	if !replace {
		// A taken name is the common case for CreateTaskFile's callers: answer it before uploading.
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_files WHERE task_id=$1 AND name=$2)`, taskID, name).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			return nil, store.ErrTaskFileExists
		}
	}
	id := uuid.New()
	kind, row, err := s.putBytes(ctx, id, data)
	if err != nil {
		return nil, err
	}
	f := &store.TaskFile{}
	var old *storedBytes
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var prev storedBytes
		err := tx.QueryRow(ctx, `SELECT id, storage FROM task_files WHERE task_id=$1 AND name=$2 FOR UPDATE`, taskID, name).Scan(&prev.ID, &prev.Kind)
		switch {
		case err == nil:
			old = &prev
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if old != nil && !replace {
			return store.ErrTaskFileExists
		}
		onConflict := `DO UPDATE SET id=EXCLUDED.id, mime_type=EXCLUDED.mime_type,
				size_bytes=EXCLUDED.size_bytes, data=EXCLUDED.data, storage=EXCLUDED.storage,
				created_by=EXCLUDED.created_by, updated_at=NOW()`
		if !replace {
			onConflict = `DO NOTHING` // a create racing this one won
		}
		var stored string
		err = tx.QueryRow(ctx, `INSERT INTO task_files (id, task_id, name, mime_type, size_bytes, data, storage, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (task_id, name) `+onConflict+`
			RETURNING `+taskFileColumns+`, storage`,
			id, taskID, name, mimeType, len(data), row, kind, createdBy).Scan(taskFileDest(f, &stored)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrTaskFileExists
		}
		return err
	})
	if err != nil {
		_ = s.dropBytes(ctx, kind, id)
		var pg *pgconn.PgError
		if asPg(err, &pg) && pg.Code == "23503" {
			return nil, domain.ErrNotFound // no such task
		}
		return nil, err
	}
	// A create racing this one can replace a row the lookup didn't see yet; its bytes are then
	// orphaned, never read. Everything else that was replaced is deleted here.
	if old != nil {
		_ = s.dropAllBytes(ctx, []storedBytes{*old})
	}
	return f, nil
}

// lineage is the task and its ancestors, nearest first, with their depth (0 for the task itself).
const lineageSQL = `WITH RECURSIVE lineage AS (
	SELECT id, parent_id, 0 AS depth FROM tasks WHERE id=$1
	UNION ALL SELECT t.id, t.parent_id, l.depth+1 FROM tasks t JOIN lineage l ON t.id=l.parent_id WHERE l.depth < 20
) `

// ListTaskFiles returns up to limit of the files taskID can read: first its own and its ancestors',
// by name, where a name that several of them have resolves to the nearest one, as OpenTaskFile does;
// then every file of its direct subtasks, by name and task number, even where the name is taken
// above. TaskID and TaskNumber tell whose it is. total is how many there are in all.
func (s *Store) ListTaskFiles(ctx context.Context, taskID uuid.UUID, limit int) (files []*store.ListedTaskFile, total int, err error) {
	head := `CASE WHEN f.storage = 'db' THEN substring(f.data FROM 1 FOR $3) END`
	rows, err := s.pool.Query(ctx, lineageSQL+`SELECT *, count(*) OVER () FROM (
			SELECT * FROM (
				SELECT DISTINCT ON (f.name) `+qualify("f", taskFileColumns)+`, f.storage, t.number, `+head+`, false AS subtask
				FROM task_files f JOIN lineage l ON l.id = f.task_id JOIN tasks t ON t.id = f.task_id
				ORDER BY f.name, l.depth
			) up
			UNION ALL
			SELECT `+qualify("f", taskFileColumns)+`, f.storage, t.number, `+head+`, true
			FROM task_files f JOIN tasks t ON t.id = f.task_id WHERE t.parent_id = $1
		) visible ORDER BY subtask, name, number LIMIT $2`, taskID, limit, store.TaskFileHeadBytes)
	if err != nil {
		return nil, 0, err
	}
	type pending struct {
		f    *store.ListedTaskFile
		kind string
	}
	var blobs []pending
	files = []*store.ListedTaskFile{}
	for rows.Next() {
		f := &store.ListedTaskFile{}
		var kind string
		if err := rows.Scan(append(taskFileDest(&f.TaskFile, &kind), &f.TaskNumber, &f.Head, &f.Subtask, &total)...); err != nil {
			rows.Close()
			return nil, 0, err
		}
		files = append(files, f)
		if kind != blob.KindDB {
			blobs = append(blobs, pending{f, kind})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// Blobs: read just the head of each. A blob that can't be opened lists without one.
	for _, p := range blobs {
		rc, err := s.openBytes(ctx, p.kind, p.f.ID, nil)
		if err != nil {
			continue
		}
		head := make([]byte, store.TaskFileHeadBytes)
		n, _ := io.ReadFull(rc, head)
		rc.Close()
		p.f.Head = head[:n]
	}
	return files, total, nil
}

// AncestorTaskFile reports the nearest ancestor of taskID (not the task itself) that has a file
// named name, by number; ok is false when none has.
func (s *Store) AncestorTaskFile(ctx context.Context, taskID uuid.UUID, name string) (number int64, ok bool, err error) {
	err = s.pool.QueryRow(ctx, lineageSQL+`SELECT t.number FROM task_files f JOIN lineage l ON l.id = f.task_id
		JOIN tasks t ON t.id = f.task_id WHERE f.name=$2 AND l.depth > 0 ORDER BY l.depth LIMIT 1`, taskID, name).Scan(&number)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return number, err == nil, err
}

// OpenTaskFile opens the file name as taskID sees it (see findTaskFile). The caller closes the
// stream; it is seekable, for reading a range.
func (s *Store) OpenTaskFile(ctx context.Context, taskID, owner uuid.UUID, name string) (*store.TaskFile, io.ReadSeekCloser, error) {
	f, kind, err := s.findTaskFile(ctx, taskID, owner, name)
	if err != nil {
		return nil, nil, err
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

// DeleteTaskFile removes one of the task's own files: the row, then its bytes (see DeleteTask).
func (s *Store) DeleteTaskFile(ctx context.Context, taskID uuid.UUID, name string) error {
	var f storedBytes
	err := s.pool.QueryRow(ctx, `DELETE FROM task_files WHERE task_id=$1 AND name=$2 RETURNING id, storage`, taskID, name).Scan(&f.ID, &f.Kind)
	if err != nil {
		return NotFound(err)
	}
	if err := s.dropAllBytes(ctx, []storedBytes{f}); err != nil {
		return fmt.Errorf("task file %q removed, but its bytes could not be deleted: %w", name, err)
	}
	return nil
}

// qualify prefixes every column in a comma-separated list with alias.
func qualify(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// findTaskFile resolves name as taskID sees it and says where its bytes are. Without an owner
// (uuid.Nil) that is its own file, else the nearest ancestor's; a subtask's file is never picked. With
// one it is owner's file, if owner is taskID, an ancestor or a direct subtask (else
// ErrTaskFileNotReadable, and nothing is read).
func (s *Store) findTaskFile(ctx context.Context, taskID, owner uuid.UUID, name string) (*store.TaskFile, string, error) {
	f := &store.TaskFile{}
	var kind string
	if owner == uuid.Nil {
		err := s.pool.QueryRow(ctx, lineageSQL+`SELECT `+qualify("f", taskFileColumns)+`, f.storage
			FROM task_files f JOIN lineage l ON l.id = f.task_id WHERE f.name=$2 ORDER BY l.depth LIMIT 1`,
			taskID, name).Scan(taskFileDest(f, &kind)...)
		if err != nil {
			return nil, "", NotFound(err)
		}
		return f, kind, nil
	}
	var readable bool
	if err := s.pool.QueryRow(ctx, lineageSQL+`SELECT EXISTS (SELECT 1 FROM lineage WHERE id=$2)
		OR EXISTS (SELECT 1 FROM tasks WHERE id=$2 AND parent_id=$1)`, taskID, owner).Scan(&readable); err != nil {
		return nil, "", err
	}
	if !readable {
		return nil, "", store.ErrTaskFileNotReadable
	}
	err := s.pool.QueryRow(ctx, `SELECT `+taskFileColumns+`, storage FROM task_files WHERE task_id=$1 AND name=$2`,
		owner, name).Scan(taskFileDest(f, &kind)...)
	if err != nil {
		return nil, "", NotFound(err)
	}
	return f, kind, nil
}

// SubtasksWithFile returns the numbers of taskID's direct subtasks that have a file named name,
// ascending.
func (s *Store) SubtasksWithFile(ctx context.Context, taskID uuid.UUID, name string) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT t.number FROM task_files f JOIN tasks t ON t.id = f.task_id
		WHERE t.parent_id=$1 AND f.name=$2 ORDER BY t.number`, taskID, name)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// ReadTaskFileRange returns up to limit bytes of the file name as taskID sees it (see findTaskFile),
// starting at byte offset, plus the file. Only that range is read: a substring of the row for bytes
// in the database, a seek on a blob.
func (s *Store) ReadTaskFileRange(ctx context.Context, taskID, owner uuid.UUID, name string, offset, limit int64) (*store.TaskFile, []byte, error) {
	f, kind, err := s.findTaskFile(ctx, taskID, owner, name)
	if err != nil {
		return nil, nil, err
	}
	offset = min(max(offset, 0), f.SizeBytes)
	limit = min(limit, f.SizeBytes-offset)
	if kind == blob.KindDB {
		var data []byte
		err := s.pool.QueryRow(ctx, `SELECT substring(data FROM $2 FOR $3) FROM task_files WHERE id=$1`, f.ID, offset+1, limit).Scan(&data)
		return f, data, NotFound(err)
	}
	rc, err := s.openBytes(ctx, kind, f.ID, nil)
	if err != nil {
		return nil, nil, err
	}
	defer rc.Close()
	if _, err := rc.Seek(offset, io.SeekStart); err != nil {
		return nil, nil, err
	}
	buf := make([]byte, limit)
	n, err := io.ReadFull(rc, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, nil, err
	}
	return f, buf[:n], nil
}
