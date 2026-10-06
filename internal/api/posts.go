package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

func (s *Server) listPosts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.PostFilter{Query: q.Get("q"), Limit: queryInt(r, "limit", 50), Offset: queryInt(r, "offset", 0)}
	for _, raw := range q["status"] {
		for _, st := range strings.Split(raw, ",") {
			status := domain.Status(strings.TrimSpace(strings.ToUpper(st)))
			if !status.Valid() {
				writeError(w, http.StatusBadRequest, "invalid_argument", "unknown status "+st)
				return
			}
			f.Statuses = append(f.Statuses, status)
		}
	}
	org, err := s.reqOrg(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	f.OrgID = &org.ID
	if f.PipelineID, err = queryUUID(r, "pipeline_id"); err != nil {
		s.fail(w, err)
		return
	}
	if f.RunID, err = queryUUID(r, "run_id"); err != nil {
		s.fail(w, err)
		return
	}
	posts, total, err := s.Store.ListPosts(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": posts, "total": total})
}

func (s *Server) getPost(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	p, err := s.Store.GetPost(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
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

func (s *Server) createPost(w http.ResponseWriter, r *http.Request) {
	var req createPostReq
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	org, err := s.reqOrg(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	p, err := s.Store.CreatePost(r.Context(), store.NewPost{
		OrgID: org.ID, Title: req.Title, Content: req.Content, Platforms: req.Platforms, ThreadParts: req.ThreadParts,
		Hashtags: req.Hashtags, MediaURLs: req.MediaURLs, Accounts: req.Accounts, ImagePrompt: req.ImagePrompt,
		Topic: req.Topic, LinkURL: req.LinkURL, ScheduledAt: req.ScheduledAt, PublisherConnectorID: req.PublisherConnectorID,
		SourceContext: map[string]any{"manual": true},
	}, domain.ActorReviewer, req.Submit)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Broker.Publish("post.updated", map[string]any{"id": p.ID, "status": p.Status})
	writeJSON(w, http.StatusCreated, p)
}

// optional distinguishes an absent JSON field from an explicit null.
type optional[T any] struct {
	Set   bool
	Value *T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

type updatePostReq struct {
	Version              *int                `json:"version"`
	Title                *string             `json:"title"`
	Content              *string             `json:"content"`
	Platforms            *[]string           `json:"platforms"`
	ThreadParts          *[]string           `json:"thread_parts"`
	Hashtags             *[]string           `json:"hashtags"`
	MediaURLs            *[]string           `json:"media_urls"`
	Accounts             *[]string           `json:"accounts"`
	Topic                *string             `json:"topic"`
	LinkURL              *string             `json:"link_url"`
	ImagePrompt          *string             `json:"image_prompt"`
	ScheduledAt          optional[time.Time] `json:"scheduled_at"`
	PublisherConnectorID optional[uuid.UUID] `json:"publisher_connector_id"`
}

func (s *Server) updatePost(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req updatePostReq
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	if req.Version == nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "version is required")
		return
	}
	patch := store.PostPatch{Title: req.Title, Content: req.Content, Platforms: req.Platforms, ThreadParts: req.ThreadParts,
		Hashtags: req.Hashtags, MediaURLs: req.MediaURLs, Accounts: req.Accounts, Topic: req.Topic, LinkURL: req.LinkURL, ImagePrompt: req.ImagePrompt}
	if req.ScheduledAt.Set {
		patch.ScheduledAt = &req.ScheduledAt.Value
	}
	if req.PublisherConnectorID.Set {
		patch.PublisherConnectorID = &req.PublisherConnectorID.Value
	}
	p, err := s.Store.UpdatePost(r.Context(), id, *req.Version, patch)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Broker.Publish("post.updated", map[string]any{"id": p.ID, "status": p.Status})
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deletePost(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.DeletePost(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	s.Broker.Publish("post.deleted", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) transition(w http.ResponseWriter, r *http.Request, to domain.Status, action domain.Action, note string, opts *store.TransitionOpts) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	p, err := s.Store.Transition(r.Context(), id, to, action, domain.ActorReviewer, note, opts)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Broker.Publish("post.updated", map[string]any{"id": p.ID, "status": p.Status})
	if to == domain.StatusApproved || to == domain.StatusQueued {
		s.Dispatcher.Wake()
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) submitPost(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, domain.StatusPendingReview, domain.ActionSubmitted, "", nil)
}

func (s *Server) approvePost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ScheduledAt          optional[time.Time] `json:"scheduled_at"`
		PublisherConnectorID optional[uuid.UUID] `json:"publisher_connector_id"`
		Note                 string              `json:"note"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &req); err != nil {
			s.fail(w, err)
			return
		}
	}
	opts := &store.TransitionOpts{}
	if req.ScheduledAt.Set {
		opts.ScheduledAt = &req.ScheduledAt.Value
	}
	if req.PublisherConnectorID.Set {
		opts.PublisherConnectorID = &req.PublisherConnectorID.Value
	}
	s.transition(w, r, domain.StatusApproved, domain.ActionApproved, req.Note, opts)
}

func (s *Server) rejectPost(w http.ResponseWriter, r *http.Request) {
	// The reason is optional: it is archived with the post when given.
	var req struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &req); err != nil {
			s.fail(w, err)
			return
		}
	}
	req.Reason = strings.TrimSpace(req.Reason)
	note := req.Reason
	if note == "" {
		note = "rejected without a reason"
	}
	s.transition(w, r, domain.StatusRejected, domain.ActionRejected, note, &store.TransitionOpts{ReviewNote: &req.Reason})
}

func (s *Server) retryPost(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, domain.StatusQueued, domain.ActionRetried, "manual retry", nil)
}

func (s *Server) postAudit(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, err := s.Store.GetPost(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	logs, err := s.Store.AuditLogs(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}
