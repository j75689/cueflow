// Package blob stores media bytes outside the database. The database keeps the metadata and a
// "storage" tag per row, so rows written before a backend was chosen (storage "db") stay readable.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Kind tags where a media row's bytes live.
const (
	KindDB   = "db"
	KindFile = "file"
	KindGCS  = "gcs"
)

// Store keeps bytes by id.
type Store interface {
	Kind() string
	Put(ctx context.Context, id string, data []byte) error
	// Open returns the bytes as a seekable stream with its size; the caller closes it.
	Open(ctx context.Context, id string) (io.ReadSeekCloser, int64, error)
	Delete(ctx context.Context, id string) error
}

// Signer is implemented by backends that can hand out a time-limited direct download URL, so large
// media can bypass the server.
type Signer interface {
	SignedURL(ctx context.Context, id string, ttl time.Duration, mimeType string) (string, error)
}

// StagingPrefix is where a trusted outside writer (the Reels renderer) leaves a finished file for the
// server to adopt; a bucket lifecycle rule deletes whatever is never adopted.
const StagingPrefix = "uploads/"

// Staged describes a file waiting in the staging area.
type Staged struct {
	Size int64
	// Head is the first bytes of the file (up to 512), enough to tell what kind of file it is.
	Head []byte
}

// Adopter is implemented by backends where another party can drop a file and ask the server to take
// it over without the bytes passing through the server.
type Adopter interface {
	// Stat reports a staged file; name is "uploads/<file>".
	Stat(ctx context.Context, name string) (*Staged, error)
	// Adopt moves a staged file into the store under id and removes the staged copy.
	Adopt(ctx context.Context, name, id string) error
}

// ErrNotFound means the backend has no bytes for that id.
var ErrNotFound = errors.New("blob: not found")

// Parse builds a backend from a location such as "file:///data/media" or "gcs://bucket/prefix". An empty location means the
// database (nil).
func Parse(loc string) (Store, error) {
	if strings.TrimSpace(loc) == "" || loc == "db" {
		return nil, nil
	}
	u, err := url.Parse(loc)
	if err != nil {
		return nil, fmt.Errorf("media storage %q: %w", loc, err)
	}
	switch u.Scheme {
	case "file":
		dir := u.Host + u.Path // file:///abs -> /abs ; file://rel/dir -> rel/dir
		if dir == "" {
			return nil, fmt.Errorf("media storage %q: missing directory", loc)
		}
		return NewFile(dir)
	case "gcs":
		if u.Host == "" {
			return nil, fmt.Errorf("media storage %q: missing bucket", loc)
		}
		return NewGCS(context.Background(), u.Host, strings.Trim(u.Path, "/"))
	default:
		return nil, fmt.Errorf("media storage %q: unsupported scheme %q (use file:// or gcs://)", loc, u.Scheme)
	}
}

// File keeps one file per id in a directory.
type File struct{ dir string }

func NewFile(dir string) (*File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &File{dir: dir}, nil
}

func (f *File) Kind() string { return KindFile }

func (f *File) path(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return "", fmt.Errorf("blob: bad id %q", id)
	}
	return filepath.Join(f.dir, id), nil
}

func (f *File) Put(_ context.Context, id string, data []byte) error {
	p, err := f.path(id)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.dir, ".put-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

func (f *File) Open(_ context.Context, id string) (io.ReadSeekCloser, int64, error) {
	p, err := f.path(id)
	if err != nil {
		return nil, 0, err
	}
	fh, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	st, err := fh.Stat()
	if err != nil {
		_ = fh.Close()
		return nil, 0, err
	}
	return fh, st.Size(), nil
}

func (f *File) Delete(_ context.Context, id string) error {
	p, err := f.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
