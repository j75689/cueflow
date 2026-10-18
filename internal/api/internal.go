package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/jobs"
)

// The cloud deployment is driven from outside: Cloud Tasks POSTs each unit of work to
// /internal/work/{kind}/{id} and Cloud Scheduler POSTs /internal/tick every minute. Both carry a
// Google-signed ID token, checked by InternalAuth; without it these routes do not exist.

// internalRoutes mounts the endpoints behind auth.
func (s *Server) internalRoutes(r chi.Router) {
	if s.InternalAuth != nil {
		r.Group(func(r chi.Router) {
			r.Use(s.InternalAuth)
			r.Post("/tick", s.tick)
			r.Post("/work/{kind}/{id}", s.work)
		})
	}
	if s.MediaRegisterAuth != nil {
		r.With(s.MediaRegisterAuth).Post("/media/register", s.registerMedia)
	}
}

func (s *Server) tick(w http.ResponseWriter, r *http.Request) {
	if s.Tick == nil {
		WriteError(w, http.StatusNotFound, "not_found", "no clock configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.Tick(ctx); err != nil {
		s.Logger.Error("tick", "err", err)
		WriteError(w, http.StatusInternalServerError, "tick_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// work runs one delivery. 2xx acknowledges it; anything else makes the queue retry, so a handler
// error is an HTTP 500 and "nothing to do" is a plain 204.
func (s *Server) work(w http.ResponseWriter, r *http.Request) {
	kind := jobs.Kind(chi.URLParam(r, "kind"))
	handler, ok := s.Handlers[kind]
	if !ok {
		// Not retryable: a kind this build does not know.
		WriteError(w, http.StatusNotFound, "unknown_kind", "no handler for "+string(kind))
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_id", "bad work id")
		return
	}
	start := time.Now()
	if err := handler(r.Context(), id); err != nil {
		s.Logger.Error("work failed", "kind", kind, "id", id, "dur", time.Since(start), "err", err)
		WriteError(w, http.StatusInternalServerError, "work_failed", err.Error())
		return
	}
	s.Logger.Info("work done", "kind", kind, "id", id, "dur", time.Since(start))
	w.WriteHeader(http.StatusNoContent)
}
