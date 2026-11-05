package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// TestHooks lets the e2e suite read and set rows the product API doesn't reach (a lease that ran
// out, a run that lost its claim) without writing SQL of its own. Test use only.
//
// Columns are given as maps of column to Go value, stored as the store stores them (see arg). In a
// where map a value matches with =, a []uuid.UUID or []string matches any of its elements, and nil
// (or a nil pointer) matches NULL; an empty where map matches every row.
type TestHooks struct {
	Store *Store
}

// Update sets the set columns of the rows matching where and reports how many it changed.
func (h *TestHooks) Update(ctx context.Context, table string, set, where map[string]any) (int64, error) {
	var args []any
	cols := sortedKeys(set)
	for i, c := range cols {
		args = append(args, set[c])
		cols[i] = fmt.Sprintf("%s=$%d", ident(c), len(args))
	}
	cond, args := whereSQL(where, args)
	tag, err := h.Store.w.Exec(ctx, "UPDATE "+ident(table)+" SET "+strings.Join(cols, ", ")+cond, args...)
	return tag.RowsAffected(), err
}

// Insert adds one row; columns it leaves out take their defaults.
func (h *TestHooks) Insert(ctx context.Context, table string, row map[string]any) error {
	var args []any
	cols := sortedKeys(row)
	marks := make([]string, len(cols))
	for i, c := range cols {
		args = append(args, row[c])
		cols[i] = ident(c)
		marks[i] = fmt.Sprintf("$%d", len(args))
	}
	_, err := h.Store.w.Exec(ctx, "INSERT INTO "+ident(table)+" ("+strings.Join(cols, ", ")+") VALUES ("+
		strings.Join(marks, ", ")+")", args...)
	return err
}

// Count counts the rows matching where.
func (h *TestHooks) Count(ctx context.Context, table string, where map[string]any) (int, error) {
	cond, args := whereSQL(where, nil)
	var n int
	err := h.Store.r.QueryRow(ctx, "SELECT count(*) FROM "+ident(table)+cond, args...).Scan(&n)
	return n, err
}

// Get scans column of the first row matching where into dest; with no such row it returns
// sql.ErrNoRows.
func (h *TestHooks) Get(ctx context.Context, table, column string, where map[string]any, dest any) error {
	cond, args := whereSQL(where, nil)
	return h.Store.r.QueryRow(ctx, "SELECT "+ident(column)+" FROM "+ident(table)+cond+" LIMIT 1", args...).Scan(dest)
}

// whereSQL turns where into a WHERE clause whose parameters follow args.
func whereSQL(where map[string]any, args []any) (string, []any) {
	if len(where) == 0 {
		return "", args
	}
	var conds []string
	for _, c := range sortedKeys(where) {
		col := ident(c)
		v := where[c]
		if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && rv.IsNil() {
			v = nil // a nil *uuid.UUID field is NULL too, not = NULL, which matches nothing
		}
		switch v := v.(type) {
		case nil:
			conds = append(conds, col+" IS NULL")
		case []uuid.UUID, []string:
			args = append(args, v)
			conds = append(conds, fmt.Sprintf("%s IN (SELECT value FROM json_each($%d))", col, len(args)))
		default:
			args = append(args, v)
			conds = append(conds, fmt.Sprintf("%s=$%d", col, len(args)))
		}
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ident quotes a table or column name.
func ident(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
