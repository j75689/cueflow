package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/blob"
	"github.com/j75689/cueflow/internal/domain"
)

// Rows that carry bytes (media, task files) keep them either in their own data column (storage "db")
// or in the configured blob backend under the row's id; the row's storage column says which. These
// helpers are that shared layer: tables differ only in their metadata.

// putBytes stores data for the row id: in the blob backend when one is configured, else in the row.
// It returns the storage tag and what to write to the data column (nil when the backend holds it).
// Callers write the bytes before the row: a row that points at nothing is worse than an orphaned file.
func (s *Store) putBytes(ctx context.Context, id uuid.UUID, data []byte) (kind string, row []byte, err error) {
	if s.blobs == nil {
		return blob.KindDB, data, nil
	}
	if err := s.blobs.Put(ctx, id.String(), data); err != nil {
		return "", nil, err
	}
	return s.blobs.Kind(), nil, nil
}

// openBytes streams the bytes of row id stored as kind. inRow loads the data column, for "db"; a
// caller that already has the row's data (or never reads "db" rows) may pass nil.
func (s *Store) openBytes(ctx context.Context, kind string, id uuid.UUID, inRow func() ([]byte, error)) (io.ReadSeekCloser, error) {
	if kind == blob.KindDB {
		if inRow == nil {
			return nil, fmt.Errorf("bytes of %s are in the database row, which the caller did not load", id)
		}
		data, err := inRow()
		if err != nil {
			return nil, err
		}
		return nopCloser{bytes.NewReader(data)}, nil
	}
	b, err := s.blobFor(kind)
	if err != nil {
		return nil, err
	}
	rc, _, err := b.Open(ctx, id.String())
	if errors.Is(err, blob.ErrNotFound) {
		return nil, domain.ErrNotFound
	}
	return rc, err
}

// dropBytes deletes the blob of row id stored as kind; bytes in the row go with the row.
func (s *Store) dropBytes(ctx context.Context, kind string, id uuid.UUID) error {
	if kind == blob.KindDB {
		return nil
	}
	b, err := s.blobFor(kind)
	if err != nil {
		return err
	}
	return b.Delete(ctx, id.String())
}

// blobFor returns the backend that holds rows tagged kind.
func (s *Store) blobFor(kind string) (blob.Store, error) {
	if s.blobs != nil && s.blobs.Kind() == kind {
		return s.blobs, nil
	}
	return nil, fmt.Errorf("bytes stored in %q but that backend is not configured (CUEFLOW_MEDIA_STORAGE)", kind)
}

// storedBytes names one row's bytes: its id and where they live.
type storedBytes struct {
	ID   uuid.UUID
	Kind string
}

// dropAllBytes deletes the blobs of rows being deleted in a transaction; an error should roll it
// back so no row is lost while its bytes remain. A backend that is no longer configured is skipped:
// nothing this process can delete there, and refusing would make the rows impossible to remove.
func (s *Store) dropAllBytes(ctx context.Context, files []storedBytes) error {
	var errs []error
	for _, f := range files {
		if _, err := s.blobFor(f.Kind); f.Kind != blob.KindDB && err != nil {
			continue
		}
		if err := s.dropBytes(ctx, f.Kind, f.ID); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d of %d files' bytes could not be deleted, so the rows were kept; try again: %w",
			len(errs), len(files), errors.Join(errs...))
	}
	return nil
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }
