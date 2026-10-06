package store

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/j75689/cueflow/internal/domain"
)

var AgentRoles = []string{"manager", "researcher", "writer", "editor", "custom"}

type AgentStats struct {
	OpenTasks int `json:"open_tasks"`
	Running   int `json:"running"`
}

type Agent struct {
	ID                uuid.UUID  `json:"id"`
	OrgID             uuid.UUID  `json:"org_id"`
	Name              string     `json:"name"`
	Slug              string     `json:"slug"`
	Avatar            string     `json:"avatar"`
	Role              string     `json:"role"`
	Description       string     `json:"description"`
	Instructions      string     `json:"instructions"`
	Capabilities      []string   `json:"capabilities"`
	LLMConnectorID    *uuid.UUID `json:"llm_connector_id"`
	SearchConnectorID *uuid.UUID `json:"search_connector_id"`
	// RuntimeConnectorID selects an external ACP agent runtime; nil = built-in LLM loop.
	RuntimeConnectorID *uuid.UUID  `json:"runtime_connector_id"`
	SourceConnectorIDs []uuid.UUID `json:"source_connector_ids"`
	// ToolConnectorIDs are the external tool servers (MCP) this agent may use.
	ToolConnectorIDs []uuid.UUID `json:"tool_connector_ids"`
	Language         string      `json:"language"`
	Platforms        []string    `json:"platforms"`
	MaxTurns         int         `json:"max_turns"`
	IsActive         bool        `json:"is_active"`
	Stats            AgentStats  `json:"stats"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
}

func (a *Agent) Has(capability string) bool {
	for _, c := range a.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

const agentColumns = `a.id, a.name, a.slug, a.avatar, a.role, a.description, a.instructions, a.capabilities,
	a.llm_connector_id, a.search_connector_id, a.source_connector_ids, a.language, a.platforms, a.max_turns,
	a.is_active, a.created_at, a.updated_at, a.runtime_connector_id, a.tool_connector_ids, a.org_id,
	(SELECT count(*) FROM tasks t WHERE t.assignee_agent_id=a.id AND t.status NOT IN ('DONE','CANCELED')),
	(SELECT count(*) FROM tasks t WHERE t.assignee_agent_id=a.id AND t.status='IN_PROGRESS')`

func scanAgent(row pgx.Row) (*Agent, error) {
	var a Agent
	if err := row.Scan(&a.ID, &a.Name, &a.Slug, &a.Avatar, &a.Role, &a.Description, &a.Instructions, &a.Capabilities,
		&a.LLMConnectorID, &a.SearchConnectorID, &a.SourceConnectorIDs, &a.Language, &a.Platforms, &a.MaxTurns,
		&a.IsActive, &a.CreatedAt, &a.UpdatedAt, &a.RuntimeConnectorID, &a.ToolConnectorIDs, &a.OrgID, &a.Stats.OpenTasks, &a.Stats.Running); err != nil {
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

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Slugify derives an ASCII slug; non-ASCII names (e.g. Chinese) fall back to "agent-xxxx".
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteRune('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "agent-" + uuid.NewString()[:4]
	}
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}

// ValidCapability is injected by package crew (which owns the tool catalog).
var ValidCapability = func(string) bool { return true }

func (a *Agent) normalize() error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return invalid("name is required")
	}
	a.Slug = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(a.Slug, "@")))
	if a.Slug == "" {
		a.Slug = Slugify(a.Name)
	}
	if !slugRe.MatchString(a.Slug) {
		return invalid("slug may only contain lowercase letters, digits and dashes")
	}
	if strings.TrimSpace(a.Avatar) == "" {
		a.Avatar = "🤖"
	}
	if a.Role == "" {
		a.Role = "custom"
	}
	validRole := false
	for _, r := range AgentRoles {
		if r == a.Role {
			validRole = true
		}
	}
	if !validRole {
		return invalid(fmt.Sprintf("unknown role %q", a.Role))
	}
	seen := map[string]bool{}
	caps := []string{}
	for _, c := range a.Capabilities {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		if !ValidCapability(c) {
			return invalid(fmt.Sprintf("unknown capability %q", c))
		}
		seen[c] = true
		caps = append(caps, c)
	}
	a.Capabilities = caps
	if len(a.Platforms) > 0 {
		if err := domain.ValidatePlatforms(a.Platforms); err != nil {
			return invalid(err.Error())
		}
	} else {
		a.Platforms = []string{}
	}
	if a.Language == "" {
		a.Language = "en"
	}
	if a.MaxTurns <= 0 {
		a.MaxTurns = 12
	}
	if a.MaxTurns > 40 {
		a.MaxTurns = 40
	}
	if a.SourceConnectorIDs == nil {
		a.SourceConnectorIDs = []uuid.UUID{}
	}
	return nil
}

func dupSlug(err error) error {
	var pg *pgconn.PgError
	if ok := asPg(err, &pg); ok && pg.Code == "23505" {
		return invalid("another agent in this org already uses this slug")
	}
	return err
}

// ListAgents returns an org's agents.
func (s *Store) ListAgents(ctx context.Context, org uuid.UUID) ([]*Agent, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+agentColumns+" FROM agents a WHERE a.org_id=$1 ORDER BY a.created_at", org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAgent(ctx context.Context, id uuid.UUID) (*Agent, error) {
	a, err := scanAgent(s.pool.QueryRow(ctx, "SELECT "+agentColumns+" FROM agents a WHERE a.id=$1", id))
	return a, notFound(err)
}

// GetAgentBySlug finds an agent of an org by its @slug.
func (s *Store) GetAgentBySlug(ctx context.Context, org uuid.UUID, slug string) (*Agent, error) {
	a, err := scanAgent(s.pool.QueryRow(ctx, "SELECT "+agentColumns+" FROM agents a WHERE a.org_id=$1 AND a.slug=$2",
		org, strings.ToLower(strings.TrimPrefix(strings.TrimSpace(slug), "@"))))
	return a, notFound(err)
}

func (s *Store) CountAgents(ctx context.Context, org uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM agents WHERE org_id=$1", org).Scan(&n)
	return n, err
}

func (s *Store) CreateAgent(ctx context.Context, a *Agent) (*Agent, error) {
	if err := a.normalize(); err != nil {
		return nil, err
	}
	if a.OrgID == uuid.Nil {
		return nil, invalid("org is required")
	}
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `INSERT INTO agents (name, slug, avatar, role, description, instructions, capabilities,
		llm_connector_id, search_connector_id, source_connector_ids, language, platforms, max_turns, is_active,
		runtime_connector_id, tool_connector_ids, org_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) RETURNING id`,
		truncate(a.Name, 64), a.Slug, truncate(a.Avatar, 16), a.Role, a.Description, a.Instructions, a.Capabilities,
		a.LLMConnectorID, a.SearchConnectorID, a.SourceConnectorIDs, a.Language, a.Platforms, a.MaxTurns, a.IsActive,
		a.RuntimeConnectorID, nonNilIDs(a.ToolConnectorIDs), a.OrgID).Scan(&id)
	if err != nil {
		return nil, dupSlug(err)
	}
	return s.GetAgent(ctx, id)
}

func (s *Store) UpdateAgent(ctx context.Context, a *Agent) (*Agent, error) {
	if err := a.normalize(); err != nil {
		return nil, err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE agents SET name=$2, slug=$3, avatar=$4, role=$5, description=$6, instructions=$7,
		capabilities=$8, llm_connector_id=$9, search_connector_id=$10, source_connector_ids=$11, language=$12,
		platforms=$13, max_turns=$14, is_active=$15, runtime_connector_id=$16, tool_connector_ids=$17, updated_at=NOW() WHERE id=$1`,
		a.ID, truncate(a.Name, 64), a.Slug, truncate(a.Avatar, 16), a.Role, a.Description, a.Instructions, a.Capabilities,
		a.LLMConnectorID, a.SearchConnectorID, a.SourceConnectorIDs, a.Language, a.Platforms, a.MaxTurns, a.IsActive,
		a.RuntimeConnectorID, nonNilIDs(a.ToolConnectorIDs))
	if err != nil {
		return nil, dupSlug(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, domain.ErrNotFound
	}
	return s.GetAgent(ctx, a.ID)
}

func (s *Store) DeleteAgent(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM agents WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

type AgentMemory struct {
	ID        uuid.UUID `json:"id"`
	AgentID   uuid.UUID `json:"agent_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) AddMemory(ctx context.Context, agentID uuid.UUID, content string) (*AgentMemory, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, invalid("memory content is required")
	}
	if r := []rune(content); len(r) > 2000 {
		content = string(r[:2000])
	}
	var m AgentMemory
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_memories (agent_id, content) VALUES ($1,$2)
		RETURNING id, agent_id, content, created_at`, agentID, content).Scan(&m.ID, &m.AgentID, &m.Content, &m.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &m, nil
}

// Memories returns an agent's notes, newest first; query filters by substring.
func (s *Store) Memories(ctx context.Context, agentID uuid.UUID, query string, limit int) ([]AgentMemory, error) {
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
	out := []AgentMemory{}
	for rows.Next() {
		var m AgentMemory
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
