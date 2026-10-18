package content

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/api"
)

// handlers serves the module's REST endpoints; h.s is the core server (auth, orgs, error mapping).
type handlers struct {
	m *Module
	s *api.Server
}

func (h *handlers) listPosts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := PostFilter{Query: q.Get("q"), Limit: api.QueryInt(r, "limit", 50), Offset: api.QueryInt(r, "offset", 0)}
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
	if f.PipelineID, err = api.QueryUUID(r, "pipeline_id"); err != nil {
		h.s.Fail(w, err)
		return
	}
	if f.RunID, err = api.QueryUUID(r, "run_id"); err != nil {
		h.s.Fail(w, err)
		return
	}
	posts, total, err := h.m.Store.ListPosts(r.Context(), f)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"items": posts, "total": total})
}

func (h *handlers) getPost(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	p, err := h.m.Store.GetPost(r.Context(), id)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, p)
}

type createPostReq struct {
	Title                string     `json:"title"`
	Content              string     `json:"content"`
	Platforms            []string   `json:"platforms"`
	ThreadParts          []string   `json:"thread_parts"`
	Hashtags             []string   `json:"hashtags"`
	MediaURLs            []string   `json:"media_urls"`
	Accounts             []string   `json:"accounts"`
	Topic                string     `json:"topic"`
	LinkURL              string     `json:"link_url"`
	ImagePrompt          string     `json:"image_prompt"`
	ScheduledAt          *time.Time `json:"scheduled_at"`
	PublisherConnectorID *uuid.UUID `json:"publisher_connector_id"`
	Submit               bool       `json:"submit"`
}

func (h *handlers) createPost(w http.ResponseWriter, r *http.Request) {
	var req createPostReq
	if err := api.Decode(r, &req); err != nil {
		h.s.Fail(w, err)
		return
	}
	org, err := h.s.ReqOrg(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	p, err := h.m.Store.CreatePost(r.Context(), NewPost{
		OrgID: org.ID, Title: req.Title, Content: req.Content, Platforms: req.Platforms, ThreadParts: req.ThreadParts,
		Hashtags: req.Hashtags, MediaURLs: req.MediaURLs, Accounts: req.Accounts, ImagePrompt: req.ImagePrompt,
		Topic: req.Topic, LinkURL: req.LinkURL, ScheduledAt: req.ScheduledAt, PublisherConnectorID: req.PublisherConnectorID,
		SourceContext: map[string]any{"manual": true},
	}, ActorReviewer, req.Submit)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	h.m.Broker.Publish("post.updated", map[string]any{"id": p.ID, "status": p.Status})
	api.WriteJSON(w, http.StatusCreated, p)
}

type updatePostReq struct {
	Version              *int                    `json:"version"`
	Title                *string                 `json:"title"`
	Content              *string                 `json:"content"`
	Platforms            *[]string               `json:"platforms"`
	ThreadParts          *[]string               `json:"thread_parts"`
	Hashtags             *[]string               `json:"hashtags"`
	MediaURLs            *[]string               `json:"media_urls"`
	Accounts             *[]string               `json:"accounts"`
	Topic                *string                 `json:"topic"`
	LinkURL              *string                 `json:"link_url"`
	ImagePrompt          *string                 `json:"image_prompt"`
	ScheduledAt          api.Optional[time.Time] `json:"scheduled_at"`
	PublisherConnectorID api.Optional[uuid.UUID] `json:"publisher_connector_id"`
}

func (h *handlers) updatePost(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	var req updatePostReq
	if err := api.Decode(r, &req); err != nil {
		h.s.Fail(w, err)
		return
	}
	if req.Version == nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_argument", "version is required")
		return
	}
	patch := PostPatch{Title: req.Title, Content: req.Content, Platforms: req.Platforms, ThreadParts: req.ThreadParts,
		Hashtags: req.Hashtags, MediaURLs: req.MediaURLs, Accounts: req.Accounts, Topic: req.Topic, LinkURL: req.LinkURL, ImagePrompt: req.ImagePrompt}
	if req.ScheduledAt.Set {
		patch.ScheduledAt = &req.ScheduledAt.Value
	}
	if req.PublisherConnectorID.Set {
		patch.PublisherConnectorID = &req.PublisherConnectorID.Value
	}
	p, err := h.m.Store.UpdatePost(r.Context(), id, *req.Version, patch)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	h.m.Broker.Publish("post.updated", map[string]any{"id": p.ID, "status": p.Status})
	api.WriteJSON(w, http.StatusOK, p)
}

func (h *handlers) deletePost(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	if err := h.m.Store.DeletePost(r.Context(), id); err != nil {
		h.s.Fail(w, err)
		return
	}
	h.m.Broker.Publish("post.deleted", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) transition(w http.ResponseWriter, r *http.Request, to Status, action Action, note string, opts *TransitionOpts) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	p, err := h.m.Store.Transition(r.Context(), id, to, action, ActorReviewer, note, opts)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	h.m.Broker.Publish("post.updated", map[string]any{"id": p.ID, "status": p.Status})
	if to == StatusApproved || to == StatusQueued {
		h.m.Dispatcher.Wake()
	}
	api.WriteJSON(w, http.StatusOK, p)
}

func (h *handlers) submitPost(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, StatusPendingReview, ActionSubmitted, "", nil)
}

func (h *handlers) approvePost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ScheduledAt          api.Optional[time.Time] `json:"scheduled_at"`
		PublisherConnectorID api.Optional[uuid.UUID] `json:"publisher_connector_id"`
		Note                 string                  `json:"note"`
	}
	if r.ContentLength != 0 {
		if err := api.Decode(r, &req); err != nil {
			h.s.Fail(w, err)
			return
		}
	}
	opts := &TransitionOpts{}
	if req.ScheduledAt.Set {
		opts.ScheduledAt = &req.ScheduledAt.Value
	}
	if req.PublisherConnectorID.Set {
		opts.PublisherConnectorID = &req.PublisherConnectorID.Value
	}
	h.transition(w, r, StatusApproved, ActionApproved, req.Note, opts)
}

func (h *handlers) rejectPost(w http.ResponseWriter, r *http.Request) {
	// The reason is optional: it is archived with the post when given.
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

func (h *handlers) retryPost(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, StatusQueued, ActionRetried, "manual retry", nil)
}

func (h *handlers) postAudit(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	if _, err := h.m.Store.GetPost(r.Context(), id); err != nil {
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
