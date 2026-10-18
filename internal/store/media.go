package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/blob"
	"github.com/j75689/cueflow/internal/domain"
)

// Media is an image CueFlow stores itself (uploaded or generated).
type Media struct {
	ID          uuid.UUID  `json:"id"`
	MimeType    string     `json:"mime_type"`
	SizeBytes   int        `json:"size_bytes"`
	Source      string     `json:"source"` // upload | generated
	Prompt      string     `json:"prompt"`
	ConnectorID *uuid.UUID `json:"connector_id"`
	CreatedBy   string     `json:"created_by"`
	Data        []byte     `json:"-"`
}

type NewMedia struct {
	MimeType    string
	Data        []byte
	Source      string
	Prompt      string
	ConnectorID *uuid.UUID
	CreatedBy   string
}

func (s *Store) CreateMedia(ctx context.Context, in NewMedia) (*Media, error) {
	if len(in.Data) == 0 {
		return nil, Invalid("image is empty")
	}
	if in.CreatedBy == "" {
		in.CreatedBy = "human"
	}
	m := &Media{MimeType: in.MimeType, SizeBytes: len(in.Data), Source: in.Source, Prompt: in.Prompt,
		ConnectorID: in.ConnectorID, CreatedBy: in.CreatedBy}
	m.ID = uuid.New()
	kind, row := blob.KindDB, in.Data
	if s.blobs != nil {
		// Bytes first: a row that points at nothing is worse than an orphaned file.
		if err := s.blobs.Put(ctx, m.ID.String(), in.Data); err != nil {
			return nil, err
		}
		kind, row = s.blobs.Kind(), nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO media (id, mime_type, data, size_bytes, source, prompt, connector_id, created_by, storage)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		m.ID, in.MimeType, row, len(in.Data), in.Source, in.Prompt, in.ConnectorID, in.CreatedBy, kind)
	if err != nil {
		if s.blobs != nil {
			_ = s.blobs.Delete(ctx, m.ID.String())
		}
		return nil, err
	}
	return m, nil
}

// getMedia reads a row. Bytes are loaded only when withData is set and they live in the database.
func (s *Store) getMedia(ctx context.Context, id uuid.UUID, withData bool) (*Media, string, error) {
	m := &Media{ID: id}
	var kind string
	q := `SELECT mime_type, size_bytes, source, prompt, connector_id, created_by, storage`
	dest := []any{&m.MimeType, &m.SizeBytes, &m.Source, &m.Prompt, &m.ConnectorID, &m.CreatedBy, &kind}
	if withData {
		q += `, data`
		dest = append(dest, &m.Data)
	}
	if err := s.pool.QueryRow(ctx, q+` FROM media WHERE id=$1`, id).Scan(dest...); err != nil {
		return nil, "", NotFound(err)
	}
	return m, kind, nil
}

// blobFor returns the backend that holds rows tagged kind.
func (s *Store) blobFor(kind string) (blob.Store, error) {
	if s.blobs != nil && s.blobs.Kind() == kind {
		return s.blobs, nil
	}
	return nil, fmt.Errorf("media stored in %q but that backend is not configured (CUEFLOW_MEDIA_STORAGE)", kind)
}

// GetMedia returns an image with its bytes.
func (s *Store) GetMedia(ctx context.Context, id uuid.UUID) (*Media, error) {
	m, kind, err := s.getMedia(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if kind == blob.KindDB {
		return m, nil
	}
	b, err := s.blobFor(kind)
	if err != nil {
		return nil, err
	}
	rc, _, err := b.Open(ctx, id.String())
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	if m.Data, err = io.ReadAll(rc); err != nil {
		return nil, err
	}
	return m, nil
}

// MediaRedirect returns a short-lived direct URL for a media item when its bytes live in a backend
// that can sign URLs (GCS), so large files are served by the storage service instead of this
// process. ok is false for media kept in the database or on disk.
func (s *Store) MediaRedirect(ctx context.Context, id uuid.UUID, ttl time.Duration) (url string, ok bool, err error) {
	m, kind, err := s.getMedia(ctx, id, false)
	if err != nil {
		return "", false, err
	}
	b, berr := s.blobFor(kind)
	if berr != nil {
		return "", false, nil
	}
	signer, isSigner := b.(blob.Signer)
	if !isSigner {
		return "", false, nil
	}
	url, err = signer.SignedURL(ctx, id.String(), ttl, m.MimeType)
	return url, err == nil, err
}

// OpenMedia returns the image's metadata and a seekable stream of its bytes (for Range requests).
// The caller closes the stream.
func (s *Store) OpenMedia(ctx context.Context, id uuid.UUID) (*Media, io.ReadSeekCloser, error) {
	m, kind, err := s.getMedia(ctx, id, false)
	if err != nil {
		return nil, nil, err
	}
	if kind == blob.KindDB {
		var data []byte
		if err := s.pool.QueryRow(ctx, `SELECT data FROM media WHERE id=$1`, id).Scan(&data); err != nil {
			return nil, nil, NotFound(err)
		}
		return m, nopCloser{bytes.NewReader(data)}, nil
	}
	b, err := s.blobFor(kind)
	if err != nil {
		return nil, nil, err
	}
	rc, _, err := b.Open(ctx, id.String())
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			return nil, nil, domain.ErrNotFound
		}
		return nil, nil, err
	}
	return m, rc, nil
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

// MigrateMedia moves every row not yet on dst into it and clears the database copy. dst nil moves
// everything back into the database. It returns how many rows moved.
func (s *Store) MigrateMedia(ctx context.Context, dst blob.Store) (int, error) {
	want := blob.KindDB
	if dst != nil {
		want = dst.Kind()
	}
	rows, err := s.pool.Query(ctx, `SELECT id FROM media WHERE storage <> $1 ORDER BY created_at`, want)
	if err != nil {
		return 0, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	moved := 0
	for _, id := range ids {
		m, err := s.GetMedia(ctx, id) // reads from wherever it lives now
		if err != nil {
			return moved, fmt.Errorf("media %s: %w", id, err)
		}
		_, oldKind, err := s.getMedia(ctx, id, false)
		if err != nil {
			return moved, err
		}
		if dst != nil {
			if err := dst.Put(ctx, id.String(), m.Data); err != nil {
				return moved, fmt.Errorf("media %s: %w", id, err)
			}
			_, err = s.pool.Exec(ctx, `UPDATE media SET storage=$2, data=NULL WHERE id=$1`, id, want)
		} else {
			_, err = s.pool.Exec(ctx, `UPDATE media SET storage=$2, data=$3 WHERE id=$1`, id, want, m.Data)
		}
		if err != nil {
			return moved, err
		}
		if old, err := s.blobFor(oldKind); err == nil {
			_ = old.Delete(ctx, id.String())
		}
		moved++
	}
	return moved, nil
}

// ErrMediaInUse means a deliverable that is still going to be reviewed or acted on uses the media.
var ErrMediaInUse = errors.New("media is used by a deliverable that is not finished")

// inUseSQL is true when some unfinished post (anything but PUBLISHED or REJECTED: a FAILED post can
// be retried) refers to the media with id $1, as "/media/<id>" or an absolute URL ending the same way.
const inUseSQL = `SELECT EXISTS (SELECT 1 FROM deliverables d
	WHERE d.status NOT IN ('DONE','REJECTED') AND d.payload::text LIKE '%/media/' || $1::text || '%')`

// AdoptMedia takes over a file a trusted writer left in the bucket's staging area: it checks what
// the file is (validate gets its first bytes and size, and returns the MIME type), moves it into the
// media store and records it. The bytes never pass through this process.
func (s *Store) AdoptMedia(ctx context.Context, staged, createdBy string, validate func(head []byte, size int64) (string, error)) (*Media, error) {
	a, ok := s.blobs.(blob.Adopter)
	if !ok {
		return nil, Invalid("this server does not keep media in Google Cloud Storage, so there is nothing to adopt")
	}
	st, err := a.Stat(ctx, staged)
	if errors.Is(err, blob.ErrNotFound) {
		return nil, Invalid("no such staged file: " + staged)
	}
	if err != nil {
		return nil, Invalid(err.Error())
	}
	mt, err := validate(st.Head, st.Size)
	if err != nil {
		return nil, Invalid(err.Error())
	}
	id := uuid.New()
	if err := a.Adopt(ctx, staged, id.String()); err != nil {
		return nil, err
	}
	m := &Media{ID: id, MimeType: mt, SizeBytes: int(st.Size), Source: "upload", CreatedBy: createdBy}
	_, err = s.pool.Exec(ctx, `INSERT INTO media (id, mime_type, data, size_bytes, source, prompt, created_by, storage)
		VALUES ($1,$2,NULL,$3,$4,'',$5,$6)`, id, mt, st.Size, m.Source, createdBy, s.blobs.Kind())
	if err != nil {
		_ = s.blobs.Delete(ctx, id.String()) // do not leave an object no row points to
		return nil, err
	}
	return m, nil
}

// DeleteMedia removes a media item: its row and its bytes. It refuses (ErrMediaInUse) while an
// unfinished post still uses it.
func (s *Store) DeleteMedia(ctx context.Context, id uuid.UUID) error {
	_, kind, err := s.getMedia(ctx, id, false)
	if err != nil {
		return err
	}
	var used bool
	if err := s.pool.QueryRow(ctx, inUseSQL, id.String()).Scan(&used); err != nil {
		return err
	}
	if used {
		return ErrMediaInUse
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM media WHERE id=$1`, id); err != nil {
		return err
	}
	if kind != blob.KindDB {
		if b, err := s.blobFor(kind); err == nil {
			if err := b.Delete(ctx, id.String()); err != nil {
				return fmt.Errorf("media %s removed, but its file could not be deleted: %w", id, err)
			}
		}
	}
	return nil
}

// PruneMedia deletes media created more than olderThan ago that no unfinished post uses. With
// dryRun it only counts. It returns how many items it deleted (or would delete).
func (s *Store) PruneMedia(ctx context.Context, olderThan time.Duration, dryRun bool) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT m.id FROM media m
		WHERE m.created_at < NOW() - make_interval(secs => $1)
		  AND NOT EXISTS (SELECT 1 FROM deliverables d
			WHERE d.status NOT IN ('DONE','REJECTED') AND d.payload::text LIKE '%/media/' || m.id::text || '%')
		ORDER BY m.created_at`, olderThan.Seconds())
	if err != nil {
		return 0, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || dryRun {
		return len(ids), err
	}
	n := 0
	for _, id := range ids {
		switch err := s.DeleteMedia(ctx, id); {
		case err == nil:
			n++
		case errors.Is(err, ErrMediaInUse), errors.Is(err, domain.ErrNotFound):
			// became used, or already gone, since the list was read
		default:
			return n, err
		}
	}
	return n, nil
}
