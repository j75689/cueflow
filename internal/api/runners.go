package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/runnerapi"
	"github.com/j75689/cueflow/internal/store"
)

// --- admin side (/api/v1) ---

func (s *Server) listRunners(w http.ResponseWriter, r *http.Request) {
	runners, err := s.Store.ListRunners(r.Context())
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, runners)
}

type registrationBody struct {
	Labels []string    `json:"labels"`
	OrgIDs []uuid.UUID `json:"org_ids"`
	// TTLMinutes is how long the token can still be used; default 60, at most 24 hours.
	TTLMinutes int `json:"ttl_minutes"`
}

func (s *Server) createRegistration(w http.ResponseWriter, r *http.Request) {
	var body registrationBody
	if err := Decode(r, &body); err != nil {
		s.Fail(w, err)
		return
	}
	ttl := body.TTLMinutes
	if ttl <= 0 {
		ttl = 60
	}
	if ttl > 24*60 {
		ttl = 24 * 60
	}
	token, exp, err := s.Store.CreateRegistration(r.Context(), body.Labels, body.OrgIDs, time.Duration(ttl)*time.Minute)
	if err != nil {
		s.Fail(w, err)
		return
	}
	url := strings.TrimRight(s.machineURL(), "/")
	WriteJSON(w, http.StatusCreated, map[string]any{
		"token": token, "expires_at": exp, "url": url,
		"command": fmt.Sprintf("cueflow runner register --url %s --token %s --name <name>", url, token),
	})
}

type runnerBody struct {
	Name           *string      `json:"name"`
	Labels         *[]string    `json:"labels"`
	OrgIDs         *[]uuid.UUID `json:"org_ids"`
	IsActive       *bool        `json:"is_active"`
	MaxConcurrency *int         `json:"max_concurrency"`
}

func (s *Server) updateRunner(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	var body runnerBody
	if err := Decode(r, &body); err != nil {
		s.Fail(w, err)
		return
	}
	out, err := s.Store.UpdateRunner(r.Context(), id, store.RunnerPatch{Name: body.Name, Labels: body.Labels,
		OrgIDs: body.OrgIDs, IsActive: body.IsActive, MaxConcurrency: body.MaxConcurrency})
	if err != nil {
		s.Fail(w, err)
		return
	}
	s.Broker.PublishGlobal("runner.updated", map[string]any{"id": out.ID})
	WriteJSON(w, http.StatusOK, out)
}

func (s *Server) deleteRunner(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	if err := s.Store.DeleteRunner(r.Context(), id); err != nil {
		s.Fail(w, err)
		return
	}
	s.Broker.PublishGlobal("runner.updated", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) taskRunnerJobs(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	jobs, err := s.Store.ListTaskRunnerJobs(r.Context(), id)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, jobs)
}

// --- runner side (/api/runner/v1) ---

type runnerCtxKey struct{}

func (s *Server) runnerRoutes(r chi.Router) {
	r.Use(s.requestLog)
	r.Post("/register", s.runnerRegister)
	r.Group(func(r chi.Router) {
		r.Use(s.runnerAuth)
		r.Post("/jobs/claim", s.runnerClaim)
		r.Post("/jobs/{id}/events", s.runnerEvents)
		r.Post("/jobs/{id}/heartbeat", s.runnerHeartbeat)
		r.Post("/jobs/{id}/complete", s.runnerComplete)
	})
}

func (s *Server) runnerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		runner, err := s.Store.RunnerByToken(r.Context(), tok)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, "unauthorized", "missing, invalid or revoked runner token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), runnerCtxKey{}, runner)))
	})
}

func runnerFrom(r *http.Request) *store.Runner {
	return r.Context().Value(runnerCtxKey{}).(*store.Runner)
}

func protocolOK(w http.ResponseWriter, v int) bool {
	if v != runnerapi.ProtocolVersion {
		WriteError(w, http.StatusUpgradeRequired, "upgrade_required",
			fmt.Sprintf("this server speaks runner protocol %d; update the runner", runnerapi.ProtocolVersion))
		return false
	}
	return true
}

func (s *Server) runnerRegister(w http.ResponseWriter, r *http.Request) {
	var req runnerapi.RegisterRequest
	if err := Decode(r, &req); err != nil {
		s.Fail(w, err)
		return
	}
	if !protocolOK(w, req.Protocol) {
		return
	}
	runner, token, err := s.Store.RegisterRunner(r.Context(), req.Token, req.Name, req.Version, req.Platform, req.Labels, req.Concurrency)
	if errors.Is(err, domain.ErrNotFound) {
		WriteError(w, http.StatusUnauthorized, "invalid_registration", "the registration token is invalid, used or expired")
		return
	}
	if err != nil {
		s.Fail(w, err)
		return
	}
	s.Broker.PublishGlobal("runner.updated", map[string]any{"id": runner.ID})
	WriteJSON(w, http.StatusCreated, runnerapi.RegisterResponse{RunnerID: runner.ID.String(), Token: token,
		Name: runner.Name, Labels: runner.Labels})
}

// runnerClaim answers with a job as soon as one is available, or 204: at once when the runner asks with
// wait 0 (it polls), after up to ClaimWait otherwise (a long poll).
func (s *Server) runnerClaim(w http.ResponseWriter, r *http.Request) {
	var req runnerapi.ClaimRequest
	if err := Decode(r, &req); err != nil {
		s.Fail(w, err)
		return
	}
	if !protocolOK(w, req.Protocol) {
		return
	}
	runner := runnerFrom(r)
	wait := runnerapi.ClaimWait
	if req.Wait != nil {
		wait = max(0, min(*req.Wait, runnerapi.ClaimWait))
	}
	deadline := time.NewTimer(time.Duration(wait) * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		job, err := s.Store.ClaimRunnerJob(r.Context(), runner)
		if err != nil {
			s.Fail(w, err)
			return
		}
		if job != nil {
			var out runnerapi.Job
			if err := json.Unmarshal(job.Payload, &out); err != nil {
				_, _ = s.Store.EndRunnerJob(r.Context(), job.ID, store.JobFailed, map[string]any{"error": "bad job payload"})
				s.Fail(w, err)
				return
			}
			out.ID = job.ID.String()
			s.Crew.RunnerPickedUp(r.Context(), job.TaskID, runner.Name)
			s.Broker.Publish(s.Store.TaskOrg(r.Context(), job.TaskID), "task.updated", map[string]any{"id": job.TaskID})
			WriteJSON(w, http.StatusOK, out)
			return
		}
		if wait == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-tick.C:
		}
		// Revoking or disabling a runner takes effect even mid-poll.
		cur, err := s.Store.GetRunner(r.Context(), runner.ID)
		if err != nil || !cur.IsActive {
			WriteError(w, http.StatusUnauthorized, "unauthorized", "this runner was revoked or disabled")
			return
		}
		runner = cur
	}
}

// ownedJob loads the job a runner is talking about; a job it does not own looks like it does not exist.
func (s *Server) ownedJob(w http.ResponseWriter, r *http.Request) (*store.RunnerJob, *store.Runner, bool) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return nil, nil, false
	}
	runner := runnerFrom(r)
	job, err := s.Store.GetRunnerJob(r.Context(), id)
	if err != nil || job.RunnerID == nil || *job.RunnerID != runner.ID {
		WriteError(w, http.StatusNotFound, "not_found", "no such job")
		return nil, nil, false
	}
	return job, runner, true
}

func (s *Server) runnerEvents(w http.ResponseWriter, r *http.Request) {
	job, runner, ok := s.ownedJob(w, r)
	if !ok {
		return
	}
	var req runnerapi.EventsRequest
	if err := Decode(r, &req); err != nil {
		s.Fail(w, err)
		return
	}
	if len(req.Events) > 200 {
		req.Events = req.Events[:200]
	}
	if job.Status == store.JobRunning || job.Status == store.JobCanceling {
		s.Crew.ApplyRunnerEvents(r.Context(), job, req.Events)
	}
	status, err := s.Store.HeartbeatRunnerJob(r.Context(), job.ID, runner.ID)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, runnerapi.JobStatus{Cancel: status != store.JobRunning})
}

func (s *Server) runnerHeartbeat(w http.ResponseWriter, r *http.Request) {
	job, runner, ok := s.ownedJob(w, r)
	if !ok {
		return
	}
	status, err := s.Store.HeartbeatRunnerJob(r.Context(), job.ID, runner.ID)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, runnerapi.JobStatus{Cancel: status != store.JobRunning})
}

func (s *Server) runnerComplete(w http.ResponseWriter, r *http.Request) {
	job, runner, ok := s.ownedJob(w, r)
	if !ok {
		return
	}
	var req runnerapi.CompleteRequest
	if err := Decode(r, &req); err != nil {
		s.Fail(w, err)
		return
	}
	status := store.JobDone
	switch {
	case req.Canceled:
		status = store.JobCanceled
	case req.Error != "" || req.TimedOut:
		status = store.JobFailed
	}
	changed, err := s.Store.CompleteRunnerJob(r.Context(), job.ID, runner.ID, status, req)
	if err != nil {
		s.Fail(w, err)
		return
	}
	// A job already closed by the server (canceled, expired) is not an error for the runner.
	WriteJSON(w, http.StatusOK, map[string]any{"accepted": changed})
}
