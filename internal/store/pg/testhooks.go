package pg

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TestLockOrgClaims takes ClaimTodo's per-org claim locks in tx, so a test can stand in for a
// claimer. Test use only.
func TestLockOrgClaims(ctx context.Context, tx pgx.Tx, orgs []uuid.UUID) error {
	return lockOrgClaims(ctx, tx, orgs)
}
