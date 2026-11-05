package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/j75689/cueflow/internal/config"
	deliverablesqlite "github.com/j75689/cueflow/internal/deliverable/sqlite"
	storesqlite "github.com/j75689/cueflow/internal/store/sqlite"
)

const copyDBUsage = "usage: cueflow copy-db --from postgres://… --to sqlite:///path/cueflow.db"

// copyDBCmd handles `cueflow copy-db --from postgres://… --to sqlite:///…`: it copies a PostgreSQL
// install into an empty SQLite file, for moving a single-mode install off PostgreSQL.
func copyDBCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("copy-db", flag.ContinueOnError)
	from := fs.String("from", "", "the PostgreSQL database to copy (postgres://…)")
	to := fs.String("to", "", "the SQLite file to copy into (sqlite:///path or file:path); new or empty")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !strings.HasPrefix(*from, "postgres://") && !strings.HasPrefix(*from, "postgresql://") {
		return errors.New(copyDBUsage + "\n--from must be a postgres:// URL")
	}
	if !(&config.Config{DatabaseURL: *to}).SQLite() {
		return errors.New(copyDBUsage + "\n--to must be a sqlite: or file: URL")
	}
	return copyDB(context.Background(), *from, *to, out)
}

// copyDB copies every table of the SQLite baseline from PostgreSQL into the SQLite file, in the
// baseline's CREATE order, one write transaction per table, all read from one snapshot of the source.
// It refuses, before writing, a source at another migration than this cueflow's, and a target that
// holds data or is at another migration than the source; it fails if any table's row count differs
// afterwards.
func copyDB(ctx context.Context, from, to string, out io.Writer) error {
	tables, err := baselineTables()
	if err != nil {
		return err
	}
	src, err := pgx.Connect(ctx, from)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	defer src.Close(context.Background())
	// One read-only snapshot: rows and counts agree even if something still writes to the source.
	tx, err := src.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var srcVersion string
	if err := tx.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&srcVersion); err != nil {
		return fmt.Errorf("source: read schema_migrations (is it a cueflow database?): %w", err)
	}
	if err := checkSource(srcVersion); err != nil {
		return err
	}
	if err := checkTarget(ctx, to, tables, srcVersion); err != nil {
		return err
	}

	dst, err := storesqlite.Open(ctx, to, nil) // connector secrets are copied sealed, as they are
	if err != nil {
		return err
	}
	defer dst.Close()
	if err := fill(ctx, tx, dst, tables, out); err != nil {
		return err
	}
	fmt.Fprintf(out, "done: %d tables, row counts match\n", len(tables))
	return nil
}

// fill migrates the checked target and copies the tables into it.
func fill(ctx context.Context, tx pgx.Tx, dst *storesqlite.Store, tables []string, out io.Writer) error {
	// A new file gets the schema; one checked above is at the source's migration already.
	if err := storesqlite.Migrate(ctx, dst); err != nil {
		return fmt.Errorf("target: migrate: %w", err)
	}
	// Match every table's columns before the first row, so a source missing one fails here.
	plans := make([]tablePlan, len(tables))
	for i, table := range tables {
		var err error
		if plans[i], err = planTable(ctx, tx, dst, table); err != nil {
			return err
		}
	}
	for _, p := range plans {
		n, err := p.copy(ctx, tx, dst)
		if err != nil {
			return fmt.Errorf("copy %s: %w", p.table, err)
		}
		fmt.Fprintf(out, "copied %-24s %d row(s)\n", p.table, n)
	}

	var differ []string
	for _, table := range tables {
		var want, got int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&want); err != nil {
			return err
		}
		if err := dst.Reader().QueryRow(ctx, `SELECT count(*) FROM "`+table+`"`).Scan(&got); err != nil {
			return err
		}
		if want != got {
			differ = append(differ, fmt.Sprintf("%s: postgres %d, sqlite %d", table, want, got))
		}
	}
	if err := reportSkipped(ctx, tx, tables, out); err != nil {
		return err
	}
	if len(differ) > 0 {
		return fmt.Errorf("row counts differ after the copy: %s", strings.Join(differ, "; "))
	}
	return nil
}

var createTable = regexp.MustCompile(`(?m)^CREATE TABLE (\w+)`)

// baselineTables are the tables SQLite's baseline migration creates, in its order (parents before
// the tables that reference them).
func baselineTables() ([]string, error) {
	names, err := fs.Glob(storesqlite.Migrations, "*_baseline.sql")
	if err != nil || len(names) != 1 {
		return nil, fmt.Errorf("sqlite baseline migration: %v %v", names, err)
	}
	body, err := fs.ReadFile(storesqlite.Migrations, names[0])
	if err != nil {
		return nil, err
	}
	var tables []string
	for _, m := range createTable.FindAllSubmatch(body, -1) {
		tables = append(tables, string(m[1]))
	}
	return tables, nil
}

// checkSource refuses a source that isn't at this cueflow's last migration. Versions compare by
// their NNNN prefix: the backends name the same migration differently (0027_auto_recover,
// 0027_baseline).
func checkSource(srcVersion string) error {
	names, err := fs.Glob(storesqlite.Migrations, "*.sql")
	if err != nil || len(names) == 0 {
		return fmt.Errorf("sqlite migrations: %v %v", names, err)
	}
	last := strings.TrimSuffix(slices.Max(names), ".sql")
	if prefix(srcVersion) != prefix(last) {
		return fmt.Errorf("source is at migration %s, this cueflow at %s: migrate the source with this version first (DATABASE_URL=<source> cueflow migrate), or copy with the version the source runs", srcVersion, last)
	}
	return nil
}

// checkTarget refuses an existing target that holds data (anything but the default org its
// migration creates), is at another migration than the source, or is in use. It only reads the
// file, read-only, and leaves a missing one missing.
func checkTarget(ctx context.Context, to string, tables []string, srcVersion string) error {
	path := storesqlite.Path(to)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	// The read-only inspection doesn't see what a -wal file holds.
	if wal, err := os.Stat(path + "-wal"); err == nil && wal.Size() > 0 {
		return fmt.Errorf("target has a non-empty %s-wal: stop what uses it, or copy into a new file", path)
	}
	dst, err := storesqlite.OpenReadOnly(ctx, to)
	if err != nil {
		return err
	}
	defer dst.Close()
	r := dst.Reader()
	var objects int
	if err := r.QueryRow(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&objects); err != nil {
		return fmt.Errorf("target: %w", err)
	}
	if objects == 0 {
		return nil
	}
	var dstVersion string
	if err := r.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&dstVersion); err != nil {
		return fmt.Errorf("target is not empty and not a cueflow database (%v); copy into a new file", err)
	}
	if prefix(dstVersion) != prefix(srcVersion) {
		return fmt.Errorf("source is at migration %s, target at %s; copy into a new file", srcVersion, dstVersion)
	}
	for _, table := range tables {
		q := `SELECT count(*) FROM "` + table + `"`
		if table == "orgs" {
			q += ` WHERE slug <> 'default'`
		}
		var n int
		if err := r.QueryRow(ctx, q).Scan(&n); err != nil {
			return fmt.Errorf("target: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("target is not empty (%s has %d row(s)); copy into a new file", table, n)
		}
	}
	return nil
}

// prefix is a migration version's NNNN.
func prefix(version string) string {
	n, _, _ := strings.Cut(version, "_")
	return n
}

// tablePlan copies one table: the SQLite table's columns, read from PostgreSQL by type.
type tablePlan struct {
	table   string
	columns []string
	cols    []pgColumn
}

func planTable(ctx context.Context, tx pgx.Tx, dst *storesqlite.Store, table string) (tablePlan, error) {
	p := tablePlan{table: table}
	rows, err := dst.Reader().Query(ctx, `SELECT name FROM pragma_table_info($1) ORDER BY cid`, table)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return p, err
		}
		p.columns = append(p.columns, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return p, err
	}
	src, err := tx.Query(ctx, p.selectSQL()+` LIMIT 0`)
	if err != nil {
		return p, fmt.Errorf("source %s: %w", table, err)
	}
	fields := src.FieldDescriptions()
	src.Close()
	for i, f := range fields {
		col, err := newPGColumn(f.DataTypeOID)
		if err != nil {
			return p, fmt.Errorf("source %s.%s: %w", table, p.columns[i], err)
		}
		p.cols = append(p.cols, col)
	}
	return p, nil
}

func (p tablePlan) selectSQL() string {
	quoted := make([]string, len(p.columns))
	for i, c := range p.columns {
		quoted[i] = pgx.Identifier{c}.Sanitize()
	}
	return `SELECT ` + strings.Join(quoted, ", ") + ` FROM ` + pgx.Identifier{p.table}.Sanitize()
}

// copy writes the table's rows in one transaction. Foreign keys are checked at commit, so a row may
// come before the row it references in the same table (a subtask before its parent). The orgs copy
// first drops the default org the target's migration created: the source has its own.
func (p tablePlan) copy(ctx context.Context, tx pgx.Tx, dst *storesqlite.Store) (int, error) {
	rows, err := tx.Query(ctx, p.selectSQL())
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	marks := make([]string, len(p.columns))
	for i := range marks {
		marks[i] = fmt.Sprintf("$%d", i+1)
	}
	insert := `INSERT INTO "` + p.table + `" ("` + strings.Join(p.columns, `", "`) + `") VALUES (` + strings.Join(marks, ", ") + `)`
	dests := make([]any, len(p.cols))
	for i, c := range p.cols {
		dests[i] = c.dest
	}
	n := 0
	err = dst.Tx(ctx, func(q storesqlite.Querier) error {
		if _, err := q.Exec(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
			return err
		}
		if p.table == "orgs" {
			if _, err := q.Exec(ctx, `DELETE FROM orgs`); err != nil {
				return err
			}
		}
		vals := make([]any, len(p.cols))
		for rows.Next() {
			if err := rows.Scan(dests...); err != nil {
				return err
			}
			for i, c := range p.cols {
				if vals[i], err = c.value(); err != nil {
					return fmt.Errorf("%s: %w", p.columns[i], err)
				}
			}
			if _, err := q.Exec(ctx, insert, vals...); err != nil {
				return err
			}
			n++
		}
		return rows.Err()
	})
	return n, err
}

// pgColumn scans one PostgreSQL column into dest and hands value to the SQLite store's Querier,
// which writes Go times, ids and lists in the form its columns hold (fixed-width UTC text, lowercase
// ids, JSON arrays). JSONB is written in the canonical form the store writes.
type pgColumn struct {
	dest  any
	value func() (any, error)
}

func newPGColumn(oid uint32) (pgColumn, error) {
	switch oid {
	case pgtype.UUIDOID:
		return scanAs[*uuid.UUID](), nil
	case pgtype.UUIDArrayOID:
		return scanAs[[]uuid.UUID](), nil
	case pgtype.TimestamptzOID:
		return scanAs[*time.Time](), nil
	case pgtype.TextOID, pgtype.VarcharOID:
		return scanAs[*string](), nil
	case pgtype.TextArrayOID, pgtype.VarcharArrayOID:
		return scanAs[[]string](), nil
	case pgtype.BoolOID:
		return scanAs[*bool](), nil
	case pgtype.Int2OID, pgtype.Int4OID, pgtype.Int8OID:
		return scanAs[*int64](), nil
	case pgtype.Float4OID, pgtype.Float8OID:
		return scanAs[*float64](), nil
	case pgtype.ByteaOID:
		var v []byte
		return pgColumn{&v, func() (any, error) {
			if v == nil {
				return nil, nil
			}
			return v, nil
		}}, nil
	case pgtype.JSONBOID, pgtype.JSONOID:
		var v []byte
		return pgColumn{&v, func() (any, error) {
			if v == nil {
				return nil, nil
			}
			c, err := deliverablesqlite.Canonical(json.RawMessage(v))
			return c, err
		}}, nil
	}
	return pgColumn{}, fmt.Errorf("no SQLite form for PostgreSQL type OID %d", oid)
}

// scanAs scans into a T (nil pointer or slice for NULL) and passes it on as it is.
func scanAs[T any]() pgColumn {
	var v T
	return pgColumn{&v, func() (any, error) { return v, nil }}
}

// reportSkipped names the source tables SQLite has no table for (the retired posts, pipelines,
// pipeline_runs, audit_logs) with their row counts.
func reportSkipped(ctx context.Context, tx pgx.Tx, tables []string, out io.Writer) error {
	rows, err := tx.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = current_schema() ORDER BY tablename`)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	var skipped []string
	for _, name := range names {
		if name == "schema_migrations" || slices.Contains(tables, name) {
			continue
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{name}.Sanitize()).Scan(&n); err != nil {
			return err
		}
		skipped = append(skipped, fmt.Sprintf("%s (%d row(s))", name, n))
	}
	if len(skipped) > 0 {
		fmt.Fprintf(out, "not copied (PostgreSQL only, no SQLite table): %s\n", strings.Join(skipped, ", "))
	}
	return nil
}
