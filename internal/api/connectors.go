package api

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/store"
)

func (s *Server) listConnectors(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Store.ListConnectors(r.Context(), r.URL.Query().Get("kind"))
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, cs)
}

func (s *Server) getConnector(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	c, err := s.Store.GetConnector(r.Context(), id)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, c)
}

func (s *Server) createConnector(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string         `json:"name"`
		Type     string         `json:"type"`
		IsActive *bool          `json:"is_active"`
		Config   map[string]any `json:"config"`
	}
	if err := Decode(r, &req); err != nil {
		s.Fail(w, err)
		return
	}
	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}
	c, err := s.Store.CreateConnector(r.Context(), req.Name, req.Type, active, req.Config)
	if err != nil {
		s.Fail(w, err)
		return
	}
	s.Broker.Publish(uuid.Nil, "connector.updated", map[string]any{"id": c.ID})
	WriteJSON(w, http.StatusCreated, c)
}

func (s *Server) updateConnector(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	var req struct {
		Name     *string        `json:"name"`
		IsActive *bool          `json:"is_active"`
		Config   map[string]any `json:"config"`
	}
	if err := Decode(r, &req); err != nil {
		s.Fail(w, err)
		return
	}
	c, err := s.Store.UpdateConnector(r.Context(), id, store.ConnectorPatch{Name: req.Name, IsActive: req.IsActive, Settings: req.Config})
	if err != nil {
		s.Fail(w, err)
		return
	}
	s.Broker.Publish(uuid.Nil, "connector.updated", map[string]any{"id": c.ID})
	WriteJSON(w, http.StatusOK, c)
}

func (s *Server) deleteConnector(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	if err := s.Store.DeleteConnector(r.Context(), id); err != nil {
		s.Fail(w, err)
		return
	}
	s.Broker.Publish(uuid.Nil, "connector.updated", map[string]any{"id": id})
	_ = s.Scheduler.Reload(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testConnector(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	inst, c, err := s.Hub.Instance(ctx, id, true)
	if err != nil {
		if c == nil {
			s.Fail(w, err)
			return
		}
		_ = s.Store.SetConnectorCheck(ctx, id, false, err.Error())
		WriteJSON(w, http.StatusOK, connector.TestResult{OK: false, Message: err.Error()})
		return
	}
	defer hub.Close(inst)
	res, err := inst.Test(ctx)
	if err != nil {
		res = &connector.TestResult{OK: false, Message: err.Error()}
	}
	_ = s.Store.SetConnectorCheck(context.Background(), id, res.OK, res.Message)
	s.Broker.Publish(uuid.Nil, "connector.updated", map[string]any{"id": id})
	WriteJSON(w, http.StatusOK, res)
}

func (s *Server) connectorTools(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	inst, _, err := s.Hub.Instance(ctx, id, true)
	if err != nil {
		s.Fail(w, err)
		return
	}
	defer hub.Close(inst)
	tl, ok := inst.(connector.ToolLister)
	if !ok {
		WriteError(w, http.StatusBadRequest, "invalid_argument", "this connector does not expose MCP tools")
		return
	}
	tools, err := tl.ListTools(ctx)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, tools)
}
