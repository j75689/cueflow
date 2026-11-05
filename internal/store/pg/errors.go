package pg

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/j75689/cueflow/internal/domain"
)

// mapErr turns a driver error into the domain error it means: no rows is domain.ErrNotFound, a unique
// violation domain.ErrConflict, a foreign key violation domain.ErrReferenced (both still wrapping the
// driver's error). Anything else is returned as is.
func mapErr(err error) error {
	var pg *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.ErrNotFound
	case asPg(err, &pg) && pg.Code == "23505":
		return fmt.Errorf("%w: %w", domain.ErrConflict, err)
	case asPg(err, &pg) && pg.Code == "23503":
		return fmt.Errorf("%w: %w", domain.ErrReferenced, err)
	}
	return err
}
