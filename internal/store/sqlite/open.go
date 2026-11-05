package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"strings"

	"modernc.org/sqlite" // also registers the "sqlite" database/sql driver
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/j75689/cueflow/internal/secret"
)

// pragmas run on every connection of both pools: WAL so reads don't wait on the writer, a busy
// timeout for the rare wait that is left (a checkpoint, another process on the file), foreign keys
// on as in PostgreSQL.
const pragmas = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"

// dsn turns sqlite:///abs/path, sqlite:rel/path or a file: URI into the driver's file: URI with
// extra parameters appended.
func dsn(url, extra string) string {
	name := url
	if rest, ok := strings.CutPrefix(url, "sqlite://"); ok {
		name = "file:" + rest
	} else if rest, ok := strings.CutPrefix(url, "sqlite:"); ok {
		name = "file:" + rest
	}
	sep := "?"
	if strings.Contains(name, "?") {
		sep = "&"
	}
	return name + sep + extra
}

// OpenReadOnly opens the existing database file url names for reading only. Unlike Open it sets no
// pragma and treats the file as immutable, so it writes nothing: not the file, nor a -wal or -shm
// beside it. It ignores what a -wal file holds, so the file must not be in use. The error is
// fs.ErrNotExist when there is no such file.
func OpenReadOnly(ctx context.Context, url string) (*Store, error) {
	p, err := sql.Open("sqlite", dsn(url, "mode=ro&immutable=1"))
	if err != nil {
		return nil, err
	}
	if err := p.PingContext(ctx); err != nil {
		p.Close()
		var e *sqlite.Error
		if errors.As(err, &e) && e.Code() == sqlite3.SQLITE_CANTOPEN {
			return nil, fmt.Errorf("sqlite: open %s: %w", url, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("sqlite: open %s: %w", url, err)
	}
	return &Store{r: db{p}, w: db{p}, rp: p, wp: p}, nil
}

// File is the path of the file the store has open, as SQLite resolved url's name (percent-decoded,
// without a localhost authority or the query).
func (s *Store) File(ctx context.Context) (string, error) {
	var file string
	err := s.r.QueryRow(ctx, `SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&file)
	return file, err
}

// Open opens the database file url names, creating it if needed, and returns the store over it,
// sealing connector secrets with box. Migrate before use.
func Open(ctx context.Context, url string, box *secret.Box) (*Store, error) {
	// Every write and claim is BEGIN IMMEDIATE on the one write connection: it takes the write lock
	// up front, so a transaction never fails upgrading a read lock.
	wp, err := sql.Open("sqlite", dsn(url, pragmas+"&_txlock=immediate"))
	if err != nil {
		return nil, err
	}
	wp.SetMaxOpenConns(1)
	wp.SetMaxIdleConns(1)
	wp.SetConnMaxLifetime(0)
	wp.SetConnMaxIdleTime(0)
	// The writer connects first: it switches a new file to WAL before a read-only connection opens it.
	if err := wp.PingContext(ctx); err != nil {
		wp.Close()
		return nil, fmt.Errorf("sqlite: open %s: %w", url, err)
	}
	// query_only makes a write routed to the read pool fail at once instead of passing unnoticed.
	rp, err := sql.Open("sqlite", dsn(url, pragmas+"&_pragma=query_only(1)"))
	if err != nil {
		wp.Close()
		return nil, err
	}
	n := max(4, runtime.NumCPU())
	rp.SetMaxOpenConns(n)
	rp.SetMaxIdleConns(n)
	if err := rp.PingContext(ctx); err != nil {
		rp.Close()
		wp.Close()
		return nil, fmt.Errorf("sqlite: open %s: %w", url, err)
	}
	return &Store{r: db{rp}, w: db{wp}, rp: rp, wp: wp, box: box}, nil
}
