package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	as, err := s.Store.ListAgents(r.Context())
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
	a := &store.Agent{IsActive: true, Language: "en", MaxTurns: 12}
	body.apply(a)
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
