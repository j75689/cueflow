package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

const connectorColumns = `id, name, kind, type, is_active, config, secrets, last_status, COALESCE(last_error,''),
	last_checked_at, created_at, updated_at, tool_permissions`

func (s *Store) scanConnector(row Row) (*store.Connector, error) {
	var c store.Connector
	var kind string
	var cfg, perms []byte
	if err := row.Scan(&c.ID, &c.Name, &kind, &c.Type, &c.IsActive, &cfg, &c.SealedSecrets, &c.LastStatus,
		&c.LastError, &c.LastCheckedAt, &c.CreatedAt, &c.UpdatedAt, &perms); err != nil {
		return nil, err
	}
	c.ToolPermissions = map[string]string{}
	_ = json.Unmarshal(perms, &c.ToolPermissions)
	c.Kind = connector.Kind(kind)
	c.Config = map[string]any{}
	_ = json.Unmarshal(cfg, &c.Config)
	c.SecretsSet = []string{}
	if len(c.SealedSecrets) > 0 {
		if m, err := s.box.OpenMap(c.SealedSecrets, c.ID[:]); err == nil {
			for k, v := range m {
				if v != nil && v != "" {
					c.SecretsSet = append(c.SecretsSet, k)
				}
			}
			sort.Strings(c.SecretsSet)
		}
	}
	return &c, nil
}

func (s *Store) ListConnectors(ctx context.Context, kind string) ([]*store.Connector, error) {
	q := "SELECT " + connectorColumns + " FROM connectors"
	var args []any
	if kind != "" {
		q += " WHERE kind=$1"
		args = append(args, kind)
	}
	rows, err := s.r.Query(ctx, q+" ORDER BY kind, name", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*store.Connector{}
	for rows.Next() {
		c, err := s.scanConnector(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetConnector(ctx context.Context, id uuid.UUID) (*store.Connector, error) {
	c, err := s.scanConnector(s.r.QueryRow(ctx, "SELECT "+connectorColumns+" FROM connectors WHERE id=$1", id))
	return c, NotFound(err)
}

// CreateConnector validates settings against the type's spec, encrypts secret fields and stores it.
func (s *Store) CreateConnector(ctx context.Context, name, typ string, active bool, settings map[string]any) (*store.Connector, error) {
	spec, ok := connector.Lookup(typ)
	if !ok {
		return nil, store.Invalid(fmt.Sprintf("unknown connector type %q", typ))
	}
	if strings.TrimSpace(name) == "" {
		name = spec.NameFor(connector.Config(settings))
	}
	cfg, secrets := spec.Split(settings)
	dropEmpty(secrets)
	if err := spec.Validate(spec.WithDefaults(merge(cfg, secrets))); err != nil {
		return nil, store.Invalid(err.Error())
	}
	id := uuid.New()
	sealed, err := s.box.SealMap(secrets, id[:])
	if err != nil {
		return nil, err
	}
	rawCfg, _ := json.Marshal(cfg)
	c, err := s.scanConnector(s.w.QueryRow(ctx, `INSERT INTO connectors (id, name, kind, type, config, secrets, is_active, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$now,$now) RETURNING `+connectorColumns, id, truncate(name, 64), string(spec.Kind), typ, string(rawCfg), sealed, active))
	return c, err
}

func (s *Store) UpdateConnector(ctx context.Context, id uuid.UUID, patch store.ConnectorPatch) (*store.Connector, error) {
	var out *store.Connector
	err := s.Tx(ctx, func(tx Querier) error {
		cur, err := s.scanConnector(tx.QueryRow(ctx, "SELECT "+connectorColumns+" FROM connectors WHERE id=$1", id))
		if err != nil {
			return NotFound(err)
		}
		spec, ok := connector.Lookup(cur.Type)
		if !ok {
			return store.Invalid(fmt.Sprintf("connector type %q is no longer installed", cur.Type))
		}
		cfg := cur.Config
		secrets, err := s.box.OpenMap(cur.SealedSecrets, id[:])
		if err != nil {
			return err
		}
		if patch.Settings != nil {
			for k, v := range patch.Settings {
				f, known := fieldOf(spec, k)
				if !known {
					continue
				}
				target := cfg
				if f.Secret {
					target = secrets
					if v == "" {
						continue // keep stored secret
					}
				}
				if v == nil {
					delete(target, k)
				} else {
					target[k] = v
				}
			}
		}
		if err := spec.Validate(spec.WithDefaults(merge(cfg, secrets))); err != nil {
			return store.Invalid(err.Error())
		}
		name, active := cur.Name, cur.IsActive
		if patch.Name != nil && strings.TrimSpace(*patch.Name) != "" {
			name = strings.TrimSpace(*patch.Name)
		}
		if patch.IsActive != nil {
			active = *patch.IsActive
		}
		sealed, err := s.box.SealMap(secrets, id[:])
		if err != nil {
			return err
		}
		rawCfg, _ := json.Marshal(cfg)
		out, err = s.scanConnector(tx.QueryRow(ctx, `UPDATE connectors SET name=$2, is_active=$3, config=$4, secrets=$5,
			updated_at=$now WHERE id=$1 RETURNING `+connectorColumns, id, truncate(name, 64), active, string(rawCfg), sealed))
		return err
	})
	return out, err
}

func (s *Store) DeleteConnector(ctx context.Context, id uuid.UUID) error {
	tag, err := s.w.Exec(ctx, "DELETE FROM connectors WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ConnectorSettings returns the merged, decrypted settings used to build an instance.
func (s *Store) ConnectorSettings(ctx context.Context, id uuid.UUID) (*store.Connector, connector.Config, error) {
	c, err := s.GetConnector(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	secrets, err := s.box.OpenMap(c.SealedSecrets, c.ID[:])
	if err != nil {
		return nil, nil, err
	}
	return c, connector.Config(merge(c.Config, secrets)), nil
}

func (s *Store) SetConnectorCheck(ctx context.Context, id uuid.UUID, ok bool, msg string) error {
	status := "ok"
	if !ok {
		status = "error"
	} else {
		msg = ""
	}
	_, err := s.w.Exec(ctx, `UPDATE connectors SET last_status=$2, last_error=$3, last_checked_at=$now WHERE id=$1`, id, status, msg)
	return err
}

// FirstActiveConnector returns the oldest active connector of a kind, if any.
func (s *Store) FirstActiveConnector(ctx context.Context, kind connector.Kind) (*store.Connector, error) {
	c, err := s.scanConnector(s.r.QueryRow(ctx, "SELECT "+connectorColumns+
		" FROM connectors WHERE kind=$1 AND is_active ORDER BY created_at LIMIT 1", string(kind)))
	return c, NotFound(err)
}

// connectorState implements connector.StateStore on the connectors.state column.
type connectorState struct {
	s  *Store
	id uuid.UUID
}

func (s *Store) ConnectorState(id uuid.UUID) connector.StateStore {
	return &connectorState{s: s, id: id}
}

func (c *connectorState) Load(ctx context.Context) (map[string]any, error) {
	var raw []byte
	if err := c.s.r.QueryRow(ctx, "SELECT state FROM connectors WHERE id=$1", c.id).Scan(&raw); err != nil {
		return nil, NotFound(err)
	}
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return m, nil
}

func (c *connectorState) Save(ctx context.Context, state map[string]any) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = c.s.w.Exec(ctx, "UPDATE connectors SET state=$2 WHERE id=$1", c.id, string(raw))
	return err
}

func fieldOf(spec connector.Spec, key string) (connector.Field, bool) {
	for _, f := range spec.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return connector.Field{}, false
}

func merge(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func dropEmpty(m map[string]any) {
	for k, v := range m {
		if v == nil || v == "" {
			delete(m, k)
		}
	}
}

// ReencryptConnectorSecrets re-seals every connector's secrets with the primary key (reading with
// the primary or previous keys) in one transaction. If any connector cannot be decrypted it
// changes nothing and reports them, so a wrong key never destroys data.
func (s *Store) ReencryptConnectorSecrets(ctx context.Context, dryRun bool) (*store.RotationReport, error) {
	rep := &store.RotationReport{DryRun: dryRun, Undecryptable: []string{}}
	err := s.Tx(ctx, func(tx Querier) error {
		rows, err := tx.Query(ctx, `SELECT id, name, secrets FROM connectors
			WHERE secrets IS NOT NULL AND length(secrets) > 0 ORDER BY created_at`)
		if err != nil {
			return err
		}
		type row struct {
			id     uuid.UUID
			name   string
			sealed []byte
		}
		var todo []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.name, &r.sealed); err != nil {
				rows.Close()
				return err
			}
			rep.Total++
			plain, idx, err := s.box.OpenWithKeyIndex(r.sealed, r.id[:])
			switch {
			case err != nil:
				rep.Undecryptable = append(rep.Undecryptable, r.name)
			case idx == 0:
				rep.AlreadyPrimary++
			default:
				sealed, err := s.box.Seal(plain, r.id[:])
				if err != nil {
					rows.Close()
					return err
				}
				todo = append(todo, row{id: r.id, name: r.name, sealed: sealed})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(rep.Undecryptable) > 0 {
			return errUndecryptable
		}
		rep.Reencrypted = len(todo)
		if dryRun {
			return nil
		}
		for _, r := range todo {
			if _, err := tx.Exec(ctx, "UPDATE connectors SET secrets=$2, updated_at=$now WHERE id=$1", r.id, r.sealed); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errUndecryptable) {
		return rep, fmt.Errorf("%d connector(s) cannot be decrypted with the configured keys: %s; nothing was changed",
			len(rep.Undecryptable), strings.Join(rep.Undecryptable, ", "))
	}
	return rep, err
}

var errUndecryptable = errors.New("undecryptable connector secrets")

// SetToolPermissions replaces a tool server's per-tool permissions.
func (s *Store) SetToolPermissions(ctx context.Context, id uuid.UUID, perms map[string]string) error {
	raw, err := json.Marshal(perms)
	if err != nil {
		return err
	}
	tag, err := s.w.Exec(ctx, `UPDATE connectors SET tool_permissions=$2, updated_at=$now WHERE id=$1`, id, string(raw))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
