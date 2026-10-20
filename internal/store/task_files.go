package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/j75689/cueflow/internal/blob"
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

// ValidTaskFileName checks a file name: valid UTF-8, 1–200 characters, "/"-separated parts that are
// neither empty nor "." or "..", no backslashes or control characters.
func ValidTaskFileName(name string) error {
	if !utf8.ValidString(name) {
		return Invalid("a file name must be valid UTF-8")
	}
	if n := utf8.RuneCountInString(name); n == 0 || n > maxTaskFileName {
		return Invalid(fmt.Sprintf("a file name must be 1 to %d characters", maxTaskFileName))
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

// maxShortField is the size of the mime_type and created_by columns.
const maxShortField = 64

// WriteTaskFile creates or replaces the task's file name. Writing only ever touches the task's own
// files; a subtask reads its ancestors' files (OpenTaskFile) but can't change them.
//
// Every write stores its bytes under a fresh row id and the upsert swaps that id in, so a reader
// sees either the old file or the new one, never new bytes with old metadata, and a failed write
// leaves the old file untouched. The old bytes are deleted after the commit. A file's id therefore
// changes when it is replaced; refer to files by task and name.
func (s *Store) WriteTaskFile(ctx context.Context, taskID uuid.UUID, name, mimeType string, data []byte, createdBy string) (*TaskFile, error) {
	return s.writeTaskFile(ctx, taskID, name, mimeType, data, createdBy, true)
}

// ErrTaskFileExists is returned by CreateTaskFile when the task already has a file of that name.
var ErrTaskFileExists = errors.New("the task already has a file of that name")

// CreateTaskFile is WriteTaskFile that never replaces: if the task has a file of that name it
// returns ErrTaskFileExists and writes nothing.
func (s *Store) CreateTaskFile(ctx context.Context, taskID uuid.UUID, name, mimeType string, data []byte, createdBy string) (*TaskFile, error) {
	return s.writeTaskFile(ctx, taskID, name, mimeType, data, createdBy, false)
}

// CountTaskFiles counts the task's own files whose names start with prefix.
func (s *Store) CountTaskFiles(ctx context.Context, taskID uuid.UUID, prefix string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM task_files WHERE task_id=$1 AND starts_with(name, $2)`, taskID, prefix).Scan(&n)
	return n, err
}

func (s *Store) writeTaskFile(ctx context.Context, taskID uuid.UUID, name, mimeType string, data []byte, createdBy string, replace bool) (*TaskFile, error) {
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
	for field, v := range map[string]string{"MIME type": mimeType, "author": createdBy} {
		if !utf8.ValidString(v) || utf8.RuneCountInString(v) > maxShortField {
			return nil, Invalid(fmt.Sprintf("a file's %s must be valid UTF-8 of at most %d characters", field, maxShortField))
		}
	}
	if !replace {
		// A taken name is the common case for CreateTaskFile's callers: answer it before uploading.
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_files WHERE task_id=$1 AND name=$2)`, taskID, name).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			return nil, ErrTaskFileExists
		}
	}
	id := uuid.New()
	kind, row, err := s.putBytes(ctx, id, data)
	if err != nil {
		return nil, err
	}
	f := &TaskFile{}
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
			return ErrTaskFileExists
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
			id, taskID, name, mimeType, len(data), row, kind, createdBy).Scan(f.scanDest(&stored)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTaskFileExists
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

// TaskFileHeadBytes is how much of each file ListTaskFiles returns in Head.
const TaskFileHeadBytes = 400

// ListedTaskFile is a file as ListTaskFiles returns it: with the number of the task it belongs to
// and the start of its bytes, so a listing never has to open the files.
type ListedTaskFile struct {
	TaskFile
	TaskNumber int64  `json:"task_number"`
	Head       []byte `json:"-"`
}

// ListTaskFiles returns up to limit of the files taskID can read, by name: its own and its
// ancestors'. A name that several of them have resolves to the nearest one, as OpenTaskFile does;
// TaskID and TaskNumber tell whose it is. total is how many there are in all.
func (s *Store) ListTaskFiles(ctx context.Context, taskID uuid.UUID, limit int) (files []*ListedTaskFile, total int, err error) {
	rows, err := s.pool.Query(ctx, lineageSQL+`SELECT *, count(*) OVER () FROM (
			SELECT DISTINCT ON (f.name) `+qualify("f", taskFileColumns)+`, f.storage, t.number,
				CASE WHEN f.storage = 'db' THEN substring(f.data FROM 1 FOR $3) END
			FROM task_files f JOIN lineage l ON l.id = f.task_id JOIN tasks t ON t.id = f.task_id
			ORDER BY f.name, l.depth
		) visible ORDER BY name LIMIT $2`, taskID, limit, TaskFileHeadBytes)
	if err != nil {
		return nil, 0, err
	}
	type pending struct {
		f    *ListedTaskFile
		kind string
	}
	var blobs []pending
	files = []*ListedTaskFile{}
	for rows.Next() {
		f := &ListedTaskFile{}
		var kind string
		if err := rows.Scan(append(f.scanDest(&kind), &f.TaskNumber, &f.Head, &total)...); err != nil {
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
		head := make([]byte, TaskFileHeadBytes)
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

// OpenTaskFile opens the file name as taskID sees it: its own, or else the nearest ancestor's. The
// caller closes the stream; it is seekable, for reading a range.
func (s *Store) OpenTaskFile(ctx context.Context, taskID uuid.UUID, name string) (*TaskFile, io.ReadSeekCloser, error) {
	f, kind, err := s.findTaskFile(ctx, taskID, name)
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

// findTaskFile resolves name as taskID sees it (its own file, else the nearest ancestor's) and says
// where its bytes are.
func (s *Store) findTaskFile(ctx context.Context, taskID uuid.UUID, name string) (*TaskFile, string, error) {
	f := &TaskFile{}
	var kind string
	err := s.pool.QueryRow(ctx, lineageSQL+`SELECT `+qualify("f", taskFileColumns)+`, f.storage
		FROM task_files f JOIN lineage l ON l.id = f.task_id WHERE f.name=$2 ORDER BY l.depth LIMIT 1`,
		taskID, name).Scan(f.scanDest(&kind)...)
	if err != nil {
		return nil, "", NotFound(err)
	}
	return f, kind, nil
}

// ReadTaskFileRange returns up to limit bytes of the file name as taskID sees it (see OpenTaskFile),
// starting at byte offset, plus the file. Only that range is read: a substring of the row for bytes
// in the database, a seek on a blob.
func (s *Store) ReadTaskFileRange(ctx context.Context, taskID uuid.UUID, name string, offset, limit int64) (*TaskFile, []byte, error) {
	f, kind, err := s.findTaskFile(ctx, taskID, name)
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
