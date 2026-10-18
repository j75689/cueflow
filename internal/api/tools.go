package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/hub"
)

type toolPermission struct {
	connector.ExternalTool
	Risky      bool   `json:"risky"`
	Default    string `json:"default"`
	Permission string `json:"permission"`
}

// toolPermissions lists a tool server's tools with their effective permission.
func (s *Server) toolPermissions(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	inst, c, err := s.Hub.Instance(ctx, id, true)
	if err != nil {
		s.Fail(w, err)
		return
	}
	defer hub.Close(inst)
	p, ok := inst.(connector.ToolProvider)
	if !ok {
		WriteError(w, http.StatusBadRequest, "invalid_argument", "this connector is not an MCP tool server")
		return
	}
	list, err := p.ExternalTools(ctx)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	out := make([]toolPermission, 0, len(list))
	for _, t := range list {
		def := connector.DefaultPermission(t)
		perm := c.ToolPermissions[t.Name]
		if !connector.ValidPermission(perm) {
			perm = def
		}
		out = append(out, toolPermission{ExternalTool: t, Risky: connector.RiskyTool(t), Default: def, Permission: perm})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"tools": out})
}

// setToolPermissions replaces the stored permissions: {"permissions": {"tool": "allow|ask|deny"}}.
func (s *Server) setToolPermissions(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	var req struct {
		Permissions map[string]string `json:"permissions"`
	}
	if err := Decode(r, &req); err != nil {
		s.Fail(w, err)
		return
	}
	c, err := s.Store.GetConnector(r.Context(), id)
	if err != nil {
		s.Fail(w, err)
		return
	}
	if c.Kind != connector.KindTools {
		WriteError(w, http.StatusBadRequest, "invalid_argument", "this connector is not an MCP tool server")
		return
	}
	perms := map[string]string{}
	for name, p := range req.Permissions {
		if !connector.ValidPermission(p) {
			WriteError(w, http.StatusBadRequest, "invalid_argument", "permission for "+name+" must be allow, ask or deny")
			return
		}
		perms[name] = p
	}
	if err := s.Store.SetToolPermissions(r.Context(), id, perms); err != nil {
		s.Fail(w, err)
		return
	}
	s.Broker.Publish("connector.updated", map[string]any{"id": id})
	WriteJSON(w, http.StatusOK, map[string]any{"permissions": perms})
}

func (s *Server) taskApprovals(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	list, err := s.Store.ListToolApprovals(r.Context(), id)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, list)
}

// decideApproval approves (and runs) or rejects an agent's tool call: {"decision":"approve|reject","note"?}.
func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request) {
	taskID, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	approvalID, err := uuid.Parse(chi.URLParam(r, "approvalId"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_argument", "invalid approval id")
		return
	}
	var req struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if err := Decode(r, &req); err != nil {
		s.Fail(w, err)
		return
	}
	if req.Decision != "approve" && req.Decision != "reject" {
		WriteError(w, http.StatusBadRequest, "invalid_argument", "decision must be approve or reject")
		return
	}
	ap, err := s.Crew.DecideApproval(r.Context(), taskID, approvalID, req.Decision == "approve", req.Note)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, ap)
}
