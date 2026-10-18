package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/j75689/cueflow/internal/domain"
)

// Org is a team with its own agents, board, schedules, pipelines, review queue and runs.
// Connectors and media are global.
type Org struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const orgColumns = `id, name, slug, created_at, updated_at`

func scanOrg(row pgx.Row) (*Org, error) {
	var o Org
	err := row.Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt)
	return &o, err
}

func (o *Org) normalize() error {
	o.Name = strings.TrimSpace(o.Name)
	if o.Name == "" {
		return Invalid("name is required")
	}
	o.Slug = strings.ToLower(strings.TrimSpace(o.Slug))
	if o.Slug == "" {
		o.Slug = Slugify(o.Name)
		if strings.HasPrefix(o.Slug, "agent-") && !strings.HasPrefix(strings.ToLower(o.Name), "agent") {
			o.Slug = "org-" + strings.TrimPrefix(o.Slug, "agent-")
		}
	}
	if !slugRe.MatchString(o.Slug) {
		return Invalid("slug may only contain lowercase letters, digits and dashes")
	}
	return nil
}

func dupOrgSlug(err error) error {
	var pg *pgconn.PgError
	if asPg(err, &pg) && pg.Code == "23505" {
		return Invalid("another org already uses this slug")
	}
	return err
}

// ListOrgs returns every org, oldest first (the first one is the default).
func (s *Store) ListOrgs(ctx context.Context) ([]*Org, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+orgColumns+" FROM orgs ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Org{}
	for rows.Next() {
		o, err := scanOrg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) GetOrg(ctx context.Context, id uuid.UUID) (*Org, error) {
	o, err := scanOrg(s.pool.QueryRow(ctx, "SELECT "+orgColumns+" FROM orgs WHERE id=$1", id))
	return o, NotFound(err)
}

// DefaultOrg is the oldest org; requests that name no org use it.
func (s *Store) DefaultOrg(ctx context.Context) (*Org, error) {
	o, err := scanOrg(s.pool.QueryRow(ctx, "SELECT "+orgColumns+" FROM orgs ORDER BY created_at, id LIMIT 1"))
	return o, NotFound(err)
}

// ResolveOrg finds an org by id or slug; an empty ref means the default org.
func (s *Store) ResolveOrg(ctx context.Context, ref string) (*Org, error) {
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

func (s *Store) CreateOrg(ctx context.Context, o *Org) (*Org, error) {
	if err := o.normalize(); err != nil {
		return nil, err
	}
	out, err := scanOrg(s.pool.QueryRow(ctx, `INSERT INTO orgs (name, slug) VALUES ($1,$2) RETURNING `+orgColumns,
		truncate(o.Name, 64), o.Slug))
	if err != nil {
		return nil, dupOrgSlug(err)
	}
	return out, nil
}

func (s *Store) UpdateOrg(ctx context.Context, o *Org) (*Org, error) {
	if err := o.normalize(); err != nil {
		return nil, err
	}
	out, err := scanOrg(s.pool.QueryRow(ctx, `UPDATE orgs SET name=$2, slug=$3, updated_at=NOW() WHERE id=$1 RETURNING `+orgColumns,
		o.ID, truncate(o.Name, 64), o.Slug))
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
		return Invalid("the last org can't be deleted")
	}
	tag, err := s.pool.Exec(ctx, "DELETE FROM orgs WHERE id=$1", id)
	if err != nil {
		var pg *pgconn.PgError
		if asPg(err, &pg) && pg.Code == "23503" {
			return Invalid("the org still has agents, tasks, schedules, pipelines or posts; delete them first")
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
		return Invalid(what + " not found")
	}
	if err != nil {
		return err
	}
	if got != org {
		return Invalid(what + " belongs to another org")
	}
	return nil
}
