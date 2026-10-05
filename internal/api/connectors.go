package api

import (
	"context"
	"net/http"
	"time"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/store"
)

func (s *Server) listConnectors(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Store.ListConnectors(r.Context(), r.URL.Query().Get("kind"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) getConnector(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	c, err := s.Store.GetConnector(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) createConnector(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string         `json:"name"`
		Type     string         `json:"type"`
		IsActive *bool          `json:"is_active"`
		Config   map[string]any `json:"config"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}
	c, err := s.Store.CreateConnector(r.Context(), req.Name, req.Type, active, req.Config)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Broker.Publish("connector.updated", map[string]any{"id": c.ID})
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) updateConnector(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		Name     *string        `json:"name"`
		IsActive *bool          `json:"is_active"`
		Config   map[string]any `json:"config"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	c, err := s.Store.UpdateConnector(r.Context(), id, store.ConnectorPatch{Name: req.Name, IsActive: req.IsActive, Settings: req.Config})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Broker.Publish("connector.updated", map[string]any{"id": c.ID})
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) deleteConnector(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.DeleteConnector(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	s.Broker.Publish("connector.updated", map[string]any{"id": id})
	_ = s.Scheduler.Reload(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testConnector(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	inst, c, err := s.Hub.Instance(ctx, id, true)
	if err != nil {
		if c == nil {
			s.fail(w, err)
			return
		}
		_ = s.Store.SetConnectorCheck(ctx, id, false, err.Error())
		writeJSON(w, http.StatusOK, connector.TestResult{OK: false, Message: err.Error()})
		return
	}
	defer hub.Close(inst)
	res, err := inst.Test(ctx)
	if err != nil {
		res = &connector.TestResult{OK: false, Message: err.Error()}
	}
	_ = s.Store.SetConnectorCheck(context.Background(), id, res.OK, res.Message)
	s.Broker.Publish("connector.updated", map[string]any{"id": id})
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) connectorTools(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	inst, _, err := s.Hub.Instance(ctx, id, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer hub.Close(inst)
	tl, ok := inst.(connector.ToolLister)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_argument", "this connector does not expose MCP tools")
		return
	}
	tools, err := tl.ListTools(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tools)
}

func (s *Server) fetchConnector(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	c, err := s.Store.GetConnector(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if c.Kind != connector.KindSource {
		writeError(w, http.StatusBadRequest, "invalid_argument", "only source connectors can fetch")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	n, err := s.Hub.FetchSource(ctx, id, time.Now().Add(-7*24*time.Hour), 0)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	s.Broker.Publish("connector.updated", map[string]any{"id": id})
	writeJSON(w, http.StatusOK, map[string]int{"new_items": n})
}

func (s *Server) connectorItems(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	items, err := s.Store.ListItems(r.Context(), id, queryInt(r, "limit", 50))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// connectorAccounts lists the accounts a per-account publisher (e.g. CueSocial) can post to.
func (s *Server) connectorAccounts(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	inst, c, err := s.Hub.Instance(ctx, id, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer hub.Close(inst)
	spec, _ := connector.Lookup(c.Type)
	al, ok := inst.(connector.AccountLister)
	if !ok || !spec.Accounts {
		writeError(w, http.StatusBadRequest, "invalid_argument", "this connector does not post to individual accounts")
		return
	}
	accounts, err := al.ListAccounts(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, accounts)
}
