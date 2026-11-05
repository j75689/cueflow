package pg

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestLockOrgClaims takes ClaimTodo's per-org claim locks in tx, so a test can stand in for a
// claimer. Test use only.
func TestLockOrgClaims(ctx context.Context, tx pgx.Tx, orgs []uuid.UUID) error {
	return lockOrgClaims(ctx, tx, orgs)
}

// TestHooks lets the e2e suite read and set rows the product API doesn't reach (a lease that ran
// out, a run that lost its claim) without writing SQL of its own. Test use only.
//
// Columns are given as maps of column to Go value. In a where map a value matches with =, a
// []uuid.UUID or []string matches any of its elements, and nil matches NULL; an empty where map
// matches every row.
type TestHooks struct {
	Pool *pgxpool.Pool
}

// Update sets the set columns of the rows matching where and reports how many it changed.
func (h *TestHooks) Update(ctx context.Context, table string, set, where map[string]any) (int64, error) {
	var args []any
	cols := sortedKeys(set)
	for i, c := range cols {
		args = append(args, set[c])
		cols[i] = fmt.Sprintf("%s=$%d", pgx.Identifier{c}.Sanitize(), len(args))
	}
	cond, args := whereSQL(where, args)
	tag, err := h.Pool.Exec(ctx, "UPDATE "+pgx.Identifier{table}.Sanitize()+" SET "+strings.Join(cols, ", ")+cond, args...)
	return tag.RowsAffected(), err
}

// Insert adds one row; columns it leaves out take their defaults.
func (h *TestHooks) Insert(ctx context.Context, table string, row map[string]any) error {
	var args []any
	cols := sortedKeys(row)
	marks := make([]string, len(cols))
	for i, c := range cols {
		args = append(args, row[c])
		cols[i] = pgx.Identifier{c}.Sanitize()
		marks[i] = fmt.Sprintf("$%d", len(args))
	}
	_, err := h.Pool.Exec(ctx, "INSERT INTO "+pgx.Identifier{table}.Sanitize()+" ("+strings.Join(cols, ", ")+") VALUES ("+
		strings.Join(marks, ", ")+")", args...)
	return err
}

// Count counts the rows matching where.
func (h *TestHooks) Count(ctx context.Context, table string, where map[string]any) (int, error) {
	cond, args := whereSQL(where, nil)
	var n int
	err := h.Pool.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()+cond, args...).Scan(&n)
	return n, err
}

// Get scans column of the first row matching where into dest; with no such row it returns
// pgx.ErrNoRows.
func (h *TestHooks) Get(ctx context.Context, table, column string, where map[string]any, dest any) error {
	cond, args := whereSQL(where, nil)
	return h.Pool.QueryRow(ctx, "SELECT "+pgx.Identifier{column}.Sanitize()+" FROM "+pgx.Identifier{table}.Sanitize()+cond+" LIMIT 1",
		args...).Scan(dest)
}

// whereSQL turns where into a WHERE clause whose parameters follow args.
func whereSQL(where map[string]any, args []any) (string, []any) {
	if len(where) == 0 {
		return "", args
	}
	var conds []string
	for _, c := range sortedKeys(where) {
		col := pgx.Identifier{c}.Sanitize()
		switch v := where[c].(type) {
		case nil:
			conds = append(conds, col+" IS NULL")
		case []uuid.UUID, []string:
			args = append(args, v)
			conds = append(conds, fmt.Sprintf("%s = ANY($%d)", col, len(args)))
		default:
			args = append(args, v)
			conds = append(conds, fmt.Sprintf("%s=$%d", col, len(args)))
		}
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
