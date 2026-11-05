package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

const orgColumns = `id, name, slug, notifier_id, agent_concurrency, created_at, updated_at`

func scanOrg(row pgx.Row) (*store.Org, error) {
	var o store.Org
	err := row.Scan(&o.ID, &o.Name, &o.Slug, &o.NotifierID, &o.AgentConcurrency, &o.CreatedAt, &o.UpdatedAt)
	return &o, err
}

func dupOrgSlug(err error) error {
	var pg *pgconn.PgError
	if asPg(err, &pg) && pg.Code == "23505" {
		return store.Invalid("another org already uses this slug")
	}
	return err
}

// ListOrgs returns every org, oldest first (the first one is the default).
func (s *Store) ListOrgs(ctx context.Context) ([]*store.Org, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+orgColumns+" FROM orgs ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*store.Org{}
	for rows.Next() {
		o, err := scanOrg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) GetOrg(ctx context.Context, id uuid.UUID) (*store.Org, error) {
	o, err := scanOrg(s.pool.QueryRow(ctx, "SELECT "+orgColumns+" FROM orgs WHERE id=$1", id))
	return o, NotFound(err)
}

// DefaultOrg is the oldest org; requests that name no org use it.
func (s *Store) DefaultOrg(ctx context.Context) (*store.Org, error) {
	o, err := scanOrg(s.pool.QueryRow(ctx, "SELECT "+orgColumns+" FROM orgs ORDER BY created_at, id LIMIT 1"))
	return o, NotFound(err)
}

// ResolveOrg finds an org by id or slug; an empty ref means the default org.
func (s *Store) ResolveOrg(ctx context.Context, ref string) (*store.Org, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return s.DefaultOrg(ctx)
	}
	if id, err := uuid.Parse(ref); err == nil {
		return s.GetOrg(ctx, id)
	}
	o, err := scanOrg(s.pool.QueryRow(ctx, "SELECT "+orgColumns+" FROM orgs WHERE slug=$1", strings.ToLower(ref)))
	return o, NotFound(err)
}

func (s *Store) CreateOrg(ctx context.Context, o *store.Org) (*store.Org, error) {
	if err := o.Normalize(); err != nil {
		return nil, err
	}
	out, err := scanOrg(s.pool.QueryRow(ctx, `INSERT INTO orgs (name, slug) VALUES ($1,$2) RETURNING `+orgColumns,
		truncate(o.Name, 64), o.Slug))
	if err != nil {
		return nil, dupOrgSlug(err)
	}
	return out, nil
}

func (s *Store) UpdateOrg(ctx context.Context, o *store.Org) (*store.Org, error) {
	if err := o.Normalize(); err != nil {
		return nil, err
	}
	if o.NotifierID != nil {
		var kind string
		err := s.pool.QueryRow(ctx, "SELECT kind FROM connectors WHERE id=$1", *o.NotifierID).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, store.Invalid("notifier not found")
		}
		if err != nil {
			return nil, err
		}
		if kind != string(connector.KindNotifier) {
			return nil, store.Invalid("the connector is not a notifier")
		}
	}
	if c := o.AgentConcurrency; c != nil && (*c < 1 || *c > store.MaxOrgAgentConcurrency) {
		return nil, store.Invalid(fmt.Sprintf("agent_concurrency must be between 1 and %d", store.MaxOrgAgentConcurrency))
	}
	out, err := scanOrg(s.pool.QueryRow(ctx, `UPDATE orgs SET name=$2, slug=$3, notifier_id=$4, agent_concurrency=$5, updated_at=NOW()
		WHERE id=$1 RETURNING `+orgColumns, o.ID, truncate(o.Name, 64), o.Slug, o.NotifierID, o.AgentConcurrency))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, dupOrgSlug(err)
	}
	return out, nil
}

// DeleteOrg removes an empty org. The last org can't be deleted.
func (s *Store) DeleteOrg(ctx context.Context, id uuid.UUID) error {
	var n int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM orgs").Scan(&n); err != nil {
		return err
	}
	if n <= 1 {
		return store.Invalid("the last org can't be deleted")
	}
	tag, err := s.pool.Exec(ctx, "DELETE FROM orgs WHERE id=$1", id)
	if err != nil {
		var pg *pgconn.PgError
		if asPg(err, &pg) && pg.Code == "23503" {
			return store.Invalid("the org still has agents, tasks, schedules, deliverables or archived content; delete them first")
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// orgOf returns the org of a row in one of the org-scoped tables.
func orgOf(ctx context.Context, q querier, table string, id uuid.UUID) (uuid.UUID, error) {
	var org uuid.UUID
	err := q.QueryRow(ctx, "SELECT org_id FROM "+table+" WHERE id=$1", id).Scan(&org)
	return org, NotFound(err)
}

// sameOrg rejects references to a row (an agent, a parent task...) that lives in another org.
func sameOrg(ctx context.Context, q querier, org uuid.UUID, table, what string, id *uuid.UUID) error {
	if id == nil {
		return nil
	}
	got, err := orgOf(ctx, q, table, *id)
	if errors.Is(err, domain.ErrNotFound) {
		return store.Invalid(what + " not found")
	}
	if err != nil {
		return err
	}
	if got != org {
		return store.Invalid(what + " belongs to another org")
	}
	return nil
}
