package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	org, err := s.reqOrg(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	as, err := s.Store.ListAgents(r.Context(), org.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, as)
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	a, err := s.Store.GetAgent(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

type agentBody struct {
	Name               *string      `json:"name"`
	Slug               *string      `json:"slug"`
	Avatar             *string      `json:"avatar"`
	Role               *string      `json:"role"`
	Description        *string      `json:"description"`
	Instructions       *string      `json:"instructions"`
	Capabilities       *[]string    `json:"capabilities"`
	LLMConnectorID     nullableID   `json:"llm_connector_id"`
	SearchConnectorID  nullableID   `json:"search_connector_id"`
	RuntimeConnectorID nullableID   `json:"runtime_connector_id"`
	SourceConnectorIDs *[]uuid.UUID `json:"source_connector_ids"`
	ToolConnectorIDs   *[]uuid.UUID `json:"tool_connector_ids"`
	Language           *string      `json:"language"`
	Platforms          *[]string    `json:"platforms"`
	MaxTurns           *int         `json:"max_turns"`
	IsActive           *bool        `json:"is_active"`
}

func (b *agentBody) apply(a *store.Agent) {
	for _, f := range []struct {
		dst *string
		src *string
	}{{&a.Name, b.Name}, {&a.Slug, b.Slug}, {&a.Avatar, b.Avatar}, {&a.Role, b.Role}, {&a.Description, b.Description},
		{&a.Instructions, b.Instructions}, {&a.Language, b.Language}} {
		if f.src != nil {
			*f.dst = *f.src
		}
	}
	if b.Capabilities != nil {
		a.Capabilities = *b.Capabilities
	}
	if b.SourceConnectorIDs != nil {
		a.SourceConnectorIDs = *b.SourceConnectorIDs
	}
	if b.ToolConnectorIDs != nil {
		a.ToolConnectorIDs = *b.ToolConnectorIDs
	}
	if b.Platforms != nil {
		a.Platforms = *b.Platforms
	}
	if b.MaxTurns != nil {
		a.MaxTurns = *b.MaxTurns
	}
	if b.IsActive != nil {
		a.IsActive = *b.IsActive
	}
	if b.LLMConnectorID.Set {
		a.LLMConnectorID = b.LLMConnectorID.Value
	}
	if b.SearchConnectorID.Set {
		a.SearchConnectorID = b.SearchConnectorID.Value
	}
	if b.RuntimeConnectorID.Set {
		a.RuntimeConnectorID = b.RuntimeConnectorID.Value
	}
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	var body agentBody
	if err := decode(r, &body); err != nil {
		s.fail(w, err)
		return
	}
	org, err := s.reqOrg(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	a := &store.Agent{OrgID: org.ID, IsActive: true, Language: "en", MaxTurns: 12}
	body.apply(a)
	if err := s.checkToolServers(r.Context(), a.ToolConnectorIDs); err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.Store.CreateAgent(r.Context(), a)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Broker.Publish("agent.updated", map[string]any{"id": out.ID})
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var body agentBody
	if err := decode(r, &body); err != nil {
		s.fail(w, err)
		return
	}
	a, err := s.Store.GetAgent(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	body.apply(a)
	if err := s.checkToolServers(r.Context(), a.ToolConnectorIDs); err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.Store.UpdateAgent(r.Context(), a)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Broker.Publish("agent.updated", map[string]any{"id": out.ID})
	s.Crew.Wake() // an agent may have been re-enabled with TODO work waiting
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	// Stop anything it is running; its tasks become unassigned (FK ON DELETE SET NULL).
	if tasks, err := s.Store.ListTasks(r.Context(), store.TaskFilter{AssigneeID: &id, Statuses: []store.TaskStatus{store.TaskInProgress}}); err == nil {
		for _, t := range tasks {
			s.Crew.Cancel(t.ID)
		}
	}
	if err := s.Store.DeleteAgent(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	s.Broker.Publish("agent.updated", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listMemories(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ms, err := s.Store.Memories(r.Context(), id, r.URL.Query().Get("q"), queryInt(r, "limit", 100))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ms)
}

func (s *Server) addMemory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decode(r, &body); err != nil {
		s.fail(w, err)
		return
	}
	m, err := s.Store.AddMemory(r.Context(), id, body.Content)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	mid, err := uuid.Parse(chi.URLParam(r, "memoryId"))
	if err != nil {
		s.fail(w, domain.ErrNotFound)
		return
	}
	if err := s.Store.DeleteMemory(r.Context(), id, mid); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkToolServers ensures every id names an MCP tool server connector.
func (s *Server) checkToolServers(ctx context.Context, ids []uuid.UUID) error {
	for _, id := range ids {
		c, err := s.Store.GetConnector(ctx, id)
		if err != nil || c.Kind != connector.KindTools {
			return &store.ValidationError{Msg: fmt.Sprintf("connector %s is not an MCP tool server", id)}
		}
	}
	return nil
}

// --- agent tokens (self-driven external agents) ---

func (s *Server) listAgentTokens(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	tokens, err := s.Store.ListAgentTokens(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) createAgentToken(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decode(r, &body); err != nil {
		s.fail(w, err)
		return
	}
	t, plain, err := s.Store.CreateAgentToken(r.Context(), id, body.Name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": t.ID, "name": t.Name, "token": plain, "created_at": t.CreatedAt,
		"mcp_url": strings.TrimRight(s.PublicURL, "/") + MCPPath})
}

func (s *Server) revokeAgentToken(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	tid, err := uuid.Parse(chi.URLParam(r, "tokenId"))
	if err != nil {
		s.fail(w, &store.ValidationError{Msg: "bad token id"})
		return
	}
	if err := s.Store.RevokeAgentToken(r.Context(), id, tid); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
