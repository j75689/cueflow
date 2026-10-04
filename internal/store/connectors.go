package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
)

// Connector is a configured plugin instance as stored. Secret values never leave the store
// through this type; SecretsSet lists which secret keys currently hold a value.
type Connector struct {
	ID            uuid.UUID      `json:"id"`
	Name          string         `json:"name"`
	Kind          connector.Kind `json:"kind"`
	Type          string         `json:"type"`
	IsActive      bool           `json:"is_active"`
	Config        map[string]any `json:"config"`
	SecretsSet    []string       `json:"secrets_set"`
	LastStatus    *string        `json:"last_status"`
	LastError     string         `json:"last_error"`
	LastCheckedAt *time.Time     `json:"last_checked_at"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`

	secrets []byte
}

const connectorColumns = `id, name, kind, type, is_active, config, secrets, last_status, COALESCE(last_error,''),
	last_checked_at, created_at, updated_at`

func (s *Store) scanConnector(row pgx.Row) (*Connector, error) {
	var c Connector
	var kind string
	var cfg []byte
	if err := row.Scan(&c.ID, &c.Name, &kind, &c.Type, &c.IsActive, &cfg, &c.secrets, &c.LastStatus,
		&c.LastError, &c.LastCheckedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Kind = connector.Kind(kind)
	c.Config = map[string]any{}
	_ = json.Unmarshal(cfg, &c.Config)
	c.SecretsSet = []string{}
	if len(c.secrets) > 0 {
		if m, err := s.box.OpenMap(c.secrets, c.ID[:]); err == nil {
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

func (s *Store) ListConnectors(ctx context.Context, kind string) ([]*Connector, error) {
	q := "SELECT " + connectorColumns + " FROM connectors"
	var args []any
	if kind != "" {
		q += " WHERE kind=$1"
		args = append(args, kind)
	}
	rows, err := s.pool.Query(ctx, q+" ORDER BY kind, name", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Connector{}
	for rows.Next() {
		c, err := s.scanConnector(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetConnector(ctx context.Context, id uuid.UUID) (*Connector, error) {
	c, err := s.scanConnector(s.pool.QueryRow(ctx, "SELECT "+connectorColumns+" FROM connectors WHERE id=$1", id))
	return c, notFound(err)
}

// CreateConnector validates settings against the type's spec, encrypts secret fields and stores it.
func (s *Store) CreateConnector(ctx context.Context, name, typ string, active bool, settings map[string]any) (*Connector, error) {
	spec, ok := connector.Lookup(typ)
	if !ok {
		return nil, invalid(fmt.Sprintf("unknown connector type %q", typ))
	}
	if strings.TrimSpace(name) == "" {
		name = spec.Name
	}
	cfg, secrets := spec.Split(settings)
	dropEmpty(secrets)
	if err := spec.Validate(spec.WithDefaults(merge(cfg, secrets))); err != nil {
		return nil, invalid(err.Error())
	}
	id := uuid.New()
	sealed, err := s.box.SealMap(secrets, id[:])
	if err != nil {
		return nil, err
	}
	rawCfg, _ := json.Marshal(cfg)
	c, err := s.scanConnector(s.pool.QueryRow(ctx, `INSERT INTO connectors (id, name, kind, type, config, secrets, is_active)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING `+connectorColumns, id, truncate(name, 64), string(spec.Kind), typ, rawCfg, sealed, active))
	return c, err
}

// ConnectorPatch: Settings keys present with a value replace it; a null value clears it. Secret keys
// sent as "" keep the stored secret (the UI never receives secret values to echo back).
type ConnectorPatch struct {
	Name     *string
	IsActive *bool
	Settings map[string]any
}

func (s *Store) UpdateConnector(ctx context.Context, id uuid.UUID, patch ConnectorPatch) (*Connector, error) {
	var out *Connector
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := s.scanConnector(tx.QueryRow(ctx, "SELECT "+connectorColumns+" FROM connectors WHERE id=$1 FOR UPDATE", id))
		if err != nil {
			return notFound(err)
		}
		spec, ok := connector.Lookup(cur.Type)
		if !ok {
			return invalid(fmt.Sprintf("connector type %q is no longer installed", cur.Type))
		}
		cfg := cur.Config
		secrets, err := s.box.OpenMap(cur.secrets, id[:])
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
			return invalid(err.Error())
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
			updated_at=NOW() WHERE id=$1 RETURNING `+connectorColumns, id, truncate(name, 64), active, rawCfg, sealed))
		return err
	})
	return out, err
}

func (s *Store) DeleteConnector(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM connectors WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ConnectorSettings returns the merged, decrypted settings used to build an instance.
func (s *Store) ConnectorSettings(ctx context.Context, id uuid.UUID) (*Connector, connector.Config, error) {
	c, err := s.GetConnector(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	secrets, err := s.box.OpenMap(c.secrets, c.ID[:])
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
	_, err := s.pool.Exec(ctx, `UPDATE connectors SET last_status=$2, last_error=$3, last_checked_at=NOW() WHERE id=$1`, id, status, msg)
	return err
}

// FirstActiveConnector returns the oldest active connector of a kind, if any.
func (s *Store) FirstActiveConnector(ctx context.Context, kind connector.Kind) (*Connector, error) {
	c, err := s.scanConnector(s.pool.QueryRow(ctx, "SELECT "+connectorColumns+
		" FROM connectors WHERE kind=$1 AND is_active ORDER BY created_at LIMIT 1", string(kind)))
	return c, notFound(err)
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
	if err := c.s.pool.QueryRow(ctx, "SELECT state FROM connectors WHERE id=$1", c.id).Scan(&raw); err != nil {
		return nil, notFound(err)
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
	_, err = c.s.pool.Exec(ctx, "UPDATE connectors SET state=$2 WHERE id=$1", c.id, raw)
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
