package deliverable

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/api"
)

// handlers serves the module's REST endpoints; h.s is the core server (auth, orgs, error mapping).
type handlers struct {
	m *Module
	s *api.Server
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := Filter{Query: q.Get("q"), Kind: strings.TrimSpace(q.Get("kind")), Limit: api.QueryInt(r, "limit", 50), Offset: api.QueryInt(r, "offset", 0)}
	for _, raw := range q["status"] {
		for _, st := range strings.Split(raw, ",") {
			status := Status(strings.TrimSpace(strings.ToUpper(st)))
			if !status.Valid() {
				api.WriteError(w, http.StatusBadRequest, "invalid_argument", "unknown status "+st)
				return
			}
			f.Statuses = append(f.Statuses, status)
		}
	}
	org, err := h.s.ReqOrg(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	f.OrgID = &org.ID
	if f.TaskID, err = api.QueryUUID(r, "task_id"); err != nil {
		h.s.Fail(w, err)
		return
	}
	items, total, err := h.m.Store.List(r.Context(), f)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	d, err := h.m.Store.Get(r.Context(), id)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, d)
}

type createReq struct {
	Kind              string          `json:"kind"`
	Title             string          `json:"title"`
	Payload           json.RawMessage `json:"payload"`
	TaskID            *uuid.UUID      `json:"task_id"`
	ActionConnectorID *uuid.UUID      `json:"action_connector_id"`
	Submit            bool            `json:"submit"`
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := api.Decode(r, &req); err != nil {
		h.s.Fail(w, err)
		return
	}
	org, err := h.s.ReqOrg(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	d, err := h.m.Store.Create(r.Context(), New{OrgID: org.ID, TaskID: req.TaskID, Kind: req.Kind, Title: req.Title,
		Payload: req.Payload, ActionConnectorID: req.ActionConnectorID}, ActorReviewer, req.Submit)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	h.m.Broker.Publish("deliverable.updated", map[string]any{"id": d.ID, "status": d.Status})
	api.WriteJSON(w, http.StatusCreated, d)
}

type updateReq struct {
	Version           *int                    `json:"version"`
	Title             *string                 `json:"title"`
	Payload           json.RawMessage         `json:"payload"`
	ActionConnectorID api.Optional[uuid.UUID] `json:"action_connector_id"`
}

func (h *handlers) update(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	var req updateReq
	if err := api.Decode(r, &req); err != nil {
		h.s.Fail(w, err)
		return
	}
	if req.Version == nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_argument", "version is required")
		return
	}
	patch := Patch{Title: req.Title, Payload: req.Payload}
	if req.ActionConnectorID.Set {
		patch.ActionConnectorID = &req.ActionConnectorID.Value
	}
	d, err := h.m.Store.Update(r.Context(), id, *req.Version, patch, ActorReviewer, "")
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	h.m.Broker.Publish("deliverable.updated", map[string]any{"id": d.ID, "status": d.Status})
	api.WriteJSON(w, http.StatusOK, d)
}

func (h *handlers) remove(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	if err := h.m.Store.Remove(r.Context(), id); err != nil {
		h.s.Fail(w, err)
		return
	}
	h.m.Broker.Publish("deliverable.deleted", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) transition(w http.ResponseWriter, r *http.Request, to Status, action Action, note string, opts *TransitionOpts) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	d, err := h.m.Store.Transition(r.Context(), id, to, action, ActorReviewer, note, opts)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	h.m.Broker.Publish("deliverable.updated", map[string]any{"id": d.ID, "status": d.Status})
	if to == StatusApproved || to == StatusQueued {
		h.m.Dispatcher.Wake()
	}
	api.WriteJSON(w, http.StatusOK, d)
}

func (h *handlers) submit(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, StatusPendingReview, ActionSubmitted, "", nil)
}

func (h *handlers) approve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Note              string                  `json:"note"`
		ActionConnectorID api.Optional[uuid.UUID] `json:"action_connector_id"`
	}
	if r.ContentLength != 0 {
		if err := api.Decode(r, &req); err != nil {
			h.s.Fail(w, err)
			return
		}
	}
	opts := &TransitionOpts{}
	if req.ActionConnectorID.Set {
		opts.ActionConnectorID = &req.ActionConnectorID.Value
	}
	h.transition(w, r, StatusApproved, ActionApproved, req.Note, opts)
}

func (h *handlers) reject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 {
		if err := api.Decode(r, &req); err != nil {
			h.s.Fail(w, err)
			return
		}
	}
	req.Reason = strings.TrimSpace(req.Reason)
	note := req.Reason
	if note == "" {
		note = "rejected without a reason"
	}
	h.transition(w, r, StatusRejected, ActionRejected, note, &TransitionOpts{ReviewNote: &req.Reason})
}

func (h *handlers) retry(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, StatusQueued, ActionRetried, "manual retry", nil)
}

func (h *handlers) audit(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	if _, err := h.m.Store.Get(r.Context(), id); err != nil {
		h.s.Fail(w, err)
		return
	}
	logs, err := h.m.Store.AuditLogs(r.Context(), id)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, logs)
}
