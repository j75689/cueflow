package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/j75689/cueflow/internal/domain"
)

// mapErr turns a driver error into the domain error it means: no rows is domain.ErrNotFound, a unique
// or primary key violation domain.ErrConflict, a foreign key violation domain.ErrReferenced (both still
// wrapping the driver's error). Anything else is returned as is.
func mapErr(err error) error {
	var e *sqlite.Error
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.ErrNotFound
	case errors.As(err, &e) && (e.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || e.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY):
		return fmt.Errorf("%w: %w", domain.ErrConflict, err)
	case errors.As(err, &e) && e.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
		return fmt.Errorf("%w: %w", domain.ErrReferenced, err)
	}
	return err
}
