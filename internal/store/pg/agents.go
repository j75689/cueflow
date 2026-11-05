package pg

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

const agentColumns = `a.id, a.name, a.slug, a.avatar, a.role, a.description, a.instructions, a.capabilities,
	a.llm_connector_id, a.search_connector_id, a.source_connector_ids, a.language, a.platforms, a.max_turns,
	a.is_active, a.created_at, a.updated_at, a.runtime_connector_id, a.tool_connector_ids, a.org_id,
	(SELECT count(*) FROM tasks t WHERE t.assignee_agent_id=a.id AND t.status NOT IN ('DONE','CANCELED')),
	(SELECT count(*) FROM tasks t WHERE t.assignee_agent_id=a.id AND t.status='IN_PROGRESS'), a.auto_handoff, a.auto_recover`

func scanAgent(row pgx.Row) (*store.Agent, error) {
	var a store.Agent
	if err := row.Scan(&a.ID, &a.Name, &a.Slug, &a.Avatar, &a.Role, &a.Description, &a.Instructions, &a.Capabilities,
		&a.LLMConnectorID, &a.SearchConnectorID, &a.SourceConnectorIDs, &a.Language, &a.Platforms, &a.MaxTurns,
		&a.IsActive, &a.CreatedAt, &a.UpdatedAt, &a.RuntimeConnectorID, &a.ToolConnectorIDs, &a.OrgID, &a.Stats.OpenTasks, &a.Stats.Running, &a.AutoHandOff, &a.AutoRecover); err != nil {
		return nil, err
	}
	if a.Capabilities == nil {
		a.Capabilities = []string{}
	}
	if a.SourceConnectorIDs == nil {
		a.SourceConnectorIDs = []uuid.UUID{}
	}
	if a.ToolConnectorIDs == nil {
		a.ToolConnectorIDs = []uuid.UUID{}
	}
	if a.Platforms == nil {
		a.Platforms = []string{}
	}
	return &a, nil
}

// normalizeRuntime is normalize for what depends on the runtime connector's type: an external
// (self-driven) agent reports its own runs, so it never auto-recovers.
func (s *Store) normalizeRuntime(ctx context.Context, a *store.Agent) error {
	if a.RuntimeConnectorID == nil || a.AutoRecover == 0 {
		return nil
	}
	var external bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM connectors WHERE id=$1 AND type=$2)",
		*a.RuntimeConnectorID, store.ExternalRuntimeType).Scan(&external); err != nil {
		return err
	}
	if external {
		a.AutoRecover = 0
	}
	return nil
}

func dupSlug(err error) error {
	var pg *pgconn.PgError
	if ok := asPg(err, &pg); ok && pg.Code == "23505" {
		return store.Invalid("another agent in this org already uses this slug")
	}
	return err
}

// ListAgents returns an org's agents.
func (s *Store) ListAgents(ctx context.Context, org uuid.UUID) ([]*store.Agent, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+agentColumns+" FROM agents a WHERE a.org_id=$1 ORDER BY a.created_at", org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*store.Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAgent(ctx context.Context, id uuid.UUID) (*store.Agent, error) {
	a, err := scanAgent(s.pool.QueryRow(ctx, "SELECT "+agentColumns+" FROM agents a WHERE a.id=$1", id))
	return a, NotFound(err)
}

// GetAgentBySlug finds an agent of an org by its @slug.
func (s *Store) GetAgentBySlug(ctx context.Context, org uuid.UUID, slug string) (*store.Agent, error) {
	a, err := scanAgent(s.pool.QueryRow(ctx, "SELECT "+agentColumns+" FROM agents a WHERE a.org_id=$1 AND a.slug=$2",
		org, strings.ToLower(strings.TrimPrefix(strings.TrimSpace(slug), "@"))))
	return a, NotFound(err)
}

func (s *Store) CountAgents(ctx context.Context, org uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM agents WHERE org_id=$1", org).Scan(&n)
	return n, err
}

func (s *Store) CreateAgent(ctx context.Context, a *store.Agent) (*store.Agent, error) {
	if err := a.Normalize(); err != nil {
		return nil, err
	}
	if err := s.normalizeRuntime(ctx, a); err != nil {
		return nil, err
	}
	if a.OrgID == uuid.Nil {
		return nil, store.Invalid("org is required")
	}
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `INSERT INTO agents (name, slug, avatar, role, description, instructions, capabilities,
		llm_connector_id, search_connector_id, source_connector_ids, language, platforms, max_turns, is_active,
		runtime_connector_id, tool_connector_ids, org_id, auto_handoff, auto_recover)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19) RETURNING id`,
		truncate(a.Name, 64), a.Slug, truncate(a.Avatar, 16), a.Role, a.Description, a.Instructions, a.Capabilities,
		a.LLMConnectorID, a.SearchConnectorID, a.SourceConnectorIDs, a.Language, a.Platforms, a.MaxTurns, a.IsActive,
		a.RuntimeConnectorID, nonNilIDs(a.ToolConnectorIDs), a.OrgID, a.AutoHandOff, a.AutoRecover).Scan(&id)
	if err != nil {
		return nil, dupSlug(err)
	}
	return s.GetAgent(ctx, id)
}

func (s *Store) UpdateAgent(ctx context.Context, a *store.Agent) (*store.Agent, error) {
	if err := a.Normalize(); err != nil {
		return nil, err
	}
	if err := s.normalizeRuntime(ctx, a); err != nil {
		return nil, err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agents SET name=$2, slug=$3, avatar=$4, role=$5, description=$6, instructions=$7,
		capabilities=$8, llm_connector_id=$9, search_connector_id=$10, source_connector_ids=$11, language=$12,
		platforms=$13, max_turns=$14, is_active=$15, runtime_connector_id=$16, tool_connector_ids=$17, auto_handoff=$18,
		auto_recover=$19, updated_at=NOW() WHERE id=$1`,
		a.ID, truncate(a.Name, 64), a.Slug, truncate(a.Avatar, 16), a.Role, a.Description, a.Instructions, a.Capabilities,
		a.LLMConnectorID, a.SearchConnectorID, a.SourceConnectorIDs, a.Language, a.Platforms, a.MaxTurns, a.IsActive,
		a.RuntimeConnectorID, nonNilIDs(a.ToolConnectorIDs), a.AutoHandOff, a.AutoRecover)
	if err != nil {
		return nil, dupSlug(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, domain.ErrNotFound
	}
	return s.GetAgent(ctx, a.ID)
}

func (s *Store) DeleteAgent(ctx context.Context, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Its tasks become unassigned (FK ON DELETE SET NULL): like any change of assignee (see
		// UpdateTask), that ends an auto-recover backoff and starts its count again.
		if _, err := tx.Exec(ctx, "UPDATE tasks SET retry_at=NULL, recover_reset_at=NOW() WHERE assignee_agent_id=$1", id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "DELETE FROM agents WHERE id=$1", id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
}

func (s *Store) AddMemory(ctx context.Context, agentID uuid.UUID, content string) (*store.AgentMemory, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, store.Invalid("memory content is required")
	}
	if r := []rune(content); len(r) > 2000 {
		content = string(r[:2000])
	}
	var m store.AgentMemory
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_memories (agent_id, content) VALUES ($1,$2)
		RETURNING id, agent_id, content, created_at`, agentID, content).Scan(&m.ID, &m.AgentID, &m.Content, &m.CreatedAt)
	if err != nil {
		return nil, NotFound(err)
	}
	return &m, nil
}

// Memories returns an agent's notes, newest first; query filters by substring.
func (s *Store) Memories(ctx context.Context, agentID uuid.UUID, query string, limit int) ([]store.AgentMemory, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT id, agent_id, content, created_at FROM agent_memories
		WHERE agent_id=$1 AND ($2 = '' OR content ILIKE '%' || $2 || '%') ORDER BY created_at DESC LIMIT $3`,
		agentID, strings.TrimSpace(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.AgentMemory{}
	for rows.Next() {
		var m store.AgentMemory
		if err := rows.Scan(&m.ID, &m.AgentID, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) DeleteMemory(ctx context.Context, agentID, memoryID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM agent_memories WHERE id=$1 AND agent_id=$2", memoryID, agentID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func nonNilIDs(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}
