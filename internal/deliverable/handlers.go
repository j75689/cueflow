package deliverable

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/api"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
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
	h.m.Broker.Publish(d.OrgID, "deliverable.updated", map[string]any{"id": d.ID, "status": d.Status})
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
	h.m.Broker.Publish(d.OrgID, "deliverable.updated", map[string]any{"id": d.ID, "status": d.Status})
	api.WriteJSON(w, http.StatusOK, d)
}

func (h *handlers) remove(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	org := uuid.Nil
	if d, err := h.m.Store.Get(r.Context(), id); err == nil {
		org = d.OrgID
	}
	if err := h.m.Store.Remove(r.Context(), id); err != nil {
		h.s.Fail(w, err)
		return
	}
	h.m.Broker.Publish(org, "deliverable.deleted", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) transition(w http.ResponseWriter, r *http.Request, to Status, action Action, note string, opts *TransitionOpts) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	d, err := h.apply(r.Context(), id, to, action, note, opts)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	if to == StatusApproved || to == StatusQueued {
		h.m.Dispatcher.Wake()
	}
	api.WriteJSON(w, http.StatusOK, d)
}

// apply is one reviewer transition and its event; the single endpoints and bulk both go through it,
// so a rule added here (an org check, say) holds for both. Waking the dispatcher is the caller's.
func (h *handlers) apply(ctx context.Context, id uuid.UUID, to Status, action Action, note string, opts *TransitionOpts) (*Deliverable, error) {
	d, err := h.m.Store.Transition(ctx, id, to, action, ActorReviewer, note, opts)
	if err != nil {
		return nil, err
	}
	h.m.Broker.Publish(d.OrgID, "deliverable.updated", map[string]any{"id": d.ID, "status": d.Status})
	return d, nil
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

// bulkLimit is how many ids one bulk request takes: the list page's size.
const bulkLimit = 200

// bulk approves or rejects many deliverables in one request. Each id is its own transition (and
// audit entry), so one that another reviewer already moved fails alone, with the single endpoint's
// code, and the rest go through; the response says which was which. A database error stops the
// batch, and the answer is still the per-id result: what moved before it, the id it hit, and the
// ids not tried, so the caller never has to guess what the server did.
func (h *handlers) bulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string   `json:"action"`
		IDs    []string `json:"ids"`
		Note   string   `json:"note"`
		Reason string   `json:"reason"`
	}
	if err := api.Decode(r, &req); err != nil {
		h.s.Fail(w, err)
		return
	}
	var to Status
	var action Action
	var opts *TransitionOpts
	note := strings.TrimSpace(req.Note)
	switch req.Action {
	case "approve":
		to, action = StatusApproved, ActionApproved
	case "reject":
		to, action = StatusRejected, ActionRejected
		reason := strings.TrimSpace(req.Reason)
		opts = &TransitionOpts{ReviewNote: &reason}
		note = reason
		if note == "" {
			note = "rejected without a reason"
		}
	default:
		h.s.Fail(w, store.Invalid(`action must be "approve" or "reject"`))
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > bulkLimit {
		h.s.Fail(w, store.Invalid(fmt.Sprintf("ids must name 1 to %d deliverables", bulkLimit)))
		return
	}
	type failure struct {
		ID      string `json:"id"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	ok, failed := []uuid.UUID{}, []failure{}
	seen := map[uuid.UUID]bool{}
	var stopped error
	for _, raw := range req.IDs {
		if stopped != nil {
			failed = append(failed, failure{raw, "not_attempted", "not attempted after an earlier error"})
			continue
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			err = domain.ErrNotFound
		} else if seen[id] {
			continue
		} else {
			seen[id] = true
			var d *Deliverable
			if d, err = h.apply(r.Context(), id, to, action, note, opts); err == nil {
				ok = append(ok, d.ID)
				continue
			}
		}
		status, code, msg := api.ErrorStatus(err)
		if status >= http.StatusInternalServerError || code == "canceled" {
			h.s.Logger.Error("bulk transition failed", "id", raw, "err", err)
			stopped = err
		}
		failed = append(failed, failure{raw, code, msg})
	}
	if to == StatusApproved && len(ok) > 0 {
		h.m.Dispatcher.Wake()
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"ok": ok, "failed": failed})
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
