// Package api serves the REST/SSE API (docs/api.md) and the embedded dashboard.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/media"
	"github.com/j75689/cueflow/internal/pipeline"
	"github.com/j75689/cueflow/internal/scheduler"
	"github.com/j75689/cueflow/internal/store"
	"github.com/j75689/cueflow/internal/webui"
)

const Version = "0.1.0"

// Dispatcher is the part of the publish worker the API needs.
type Dispatcher interface{ Wake() }

type Server struct {
	Store      *store.Store
	Hub        *hub.Hub
	Broker     *events.Broker
	Runner     *pipeline.Runner
	Scheduler  *scheduler.Scheduler
	Dispatcher Dispatcher
	Crew       *crew.Crew
	Logger     *slog.Logger
	AdminToken string
	// MCPToken enables the operator MCP endpoint (CUEFLOW_MCP_TOKEN); empty disables it.
	MCPToken string
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.requestLog, s.auth)
		r.Get("/health", s.health)
		r.Get("/meta", s.meta)
		r.Get("/stats", s.stats)
		r.Get("/events", s.events)

		r.Get("/orgs", s.listOrgs)
		r.Post("/orgs", s.createOrg)
		r.Patch("/orgs/{id}", s.updateOrg)
		r.Delete("/orgs/{id}", s.deleteOrg)

		r.Get("/posts", s.listPosts)
		r.Post("/posts", s.createPost)
		r.Get("/posts/{id}", s.getPost)
		r.Patch("/posts/{id}", s.updatePost)
		r.Delete("/posts/{id}", s.deletePost)
		r.Post("/posts/{id}/submit", s.submitPost)
		r.Post("/posts/{id}/approve", s.approvePost)
		r.Post("/posts/{id}/reject", s.rejectPost)
		r.Post("/posts/{id}/retry", s.retryPost)
		r.Get("/posts/{id}/audit", s.postAudit)

		r.Get("/connectors", s.listConnectors)
		r.Post("/connectors", s.createConnector)
		r.Get("/connectors/{id}", s.getConnector)
		r.Patch("/connectors/{id}", s.updateConnector)
		r.Delete("/connectors/{id}", s.deleteConnector)
		r.Post("/connectors/{id}/test", s.testConnector)
		r.Get("/connectors/{id}/tools", s.connectorTools)
		r.Get("/connectors/{id}/accounts", s.connectorAccounts)
		r.Get("/connectors/{id}/tool-permissions", s.toolPermissions)
		r.Put("/connectors/{id}/tool-permissions", s.setToolPermissions)
		r.Post("/connectors/{id}/fetch", s.fetchConnector)
		r.Get("/connectors/{id}/items", s.connectorItems)

		r.Get("/pipelines", s.listPipelines)
		r.Post("/pipelines", s.createPipeline)
		r.Get("/pipelines/{id}", s.getPipeline)
		r.Patch("/pipelines/{id}", s.updatePipeline)
		r.Delete("/pipelines/{id}", s.deletePipeline)
		r.Post("/pipelines/{id}/run", s.runPipeline)
		r.Get("/runs", s.listRuns)
		r.Get("/runs/{id}", s.getRun)

		r.Get("/agents", s.listAgents)
		r.Post("/agents", s.createAgent)
		r.Get("/agents/{id}", s.getAgent)
		r.Patch("/agents/{id}", s.updateAgent)
		r.Delete("/agents/{id}", s.deleteAgent)
		r.Get("/agents/{id}/memories", s.listMemories)
		r.Post("/agents/{id}/memories", s.addMemory)
		r.Delete("/agents/{id}/memories/{memoryId}", s.deleteMemory)

		r.Get("/tasks", s.listTasks)
		r.Post("/tasks", s.createTask)
		r.Get("/tasks/{id}", s.getTask)
		r.Patch("/tasks/{id}", s.updateTask)
		r.Delete("/tasks/{id}", s.deleteTask)
		r.Get("/tasks/{id}/messages", s.taskMessages)
		r.Post("/tasks/{id}/messages", s.postTaskMessage)
		r.Post("/tasks/{id}/run", s.runTask)
		r.Post("/tasks/{id}/cancel", s.cancelTask)
		r.Get("/tasks/{id}/runs", s.taskRuns)
		r.Get("/tasks/{id}/approvals", s.taskApprovals)
		r.Post("/tasks/{id}/approvals/{approvalId}", s.decideApproval)

		r.Get("/usage", s.usage)

		r.Post("/media", s.uploadMedia)
		r.Post("/media/generate", s.generateMedia)

		r.Get("/schedules", s.listSchedules)
		r.Post("/schedules", s.createSchedule)
		r.Get("/schedules/{id}", s.getSchedule)
		r.Patch("/schedules/{id}", s.updateSchedule)
		r.Delete("/schedules/{id}", s.deleteSchedule)
		r.Post("/schedules/{id}/run", s.runSchedule)

		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
		})
	})
	// Per-run MCP tool servers for external (ACP) agents. Authenticated by the unguessable run
	// token in the path, not the admin token: the agent process never sees admin credentials.
	r.Handle(crew.MCPPathPrefix+"*", s.Crew.MCPHandler())
	// Operator MCP endpoint for external agents, authenticated by CUEFLOW_MCP_TOKEN.
	r.Handle(MCPPath, s.mcpHandler())
	// Stored images, public by unguessable id so publishers can fetch them.
	r.Get(media.PathPrefix+"{file}", s.serveMedia)
	r.Handle("/*", webui.Handler())
	return r
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if r.URL.Path == "/api/v1/events" {
			return
		}
		s.Logger.Debug("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "dur", time.Since(start))
	})
}

// auth enforces CUEFLOW_ADMIN_TOKEN when configured.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.AdminToken == "" || r.URL.Path == "/api/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" && r.URL.Path == "/api/v1/events" {
			tok = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.AdminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid admin token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	dbStatus := "ok"
	if err := s.Store.Ping(r.Context()); err != nil {
		dbStatus = err.Error()
	}
	code := http.StatusOK
	if dbStatus != "ok" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"status": "ok", "db": dbStatus, "time": time.Now().UTC()})
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	transitions := map[string][]domain.Status{}
	for k, v := range domain.Transitions() {
		transitions[string(k)] = v
	}
	specs := connector.Specs()
	for i := range specs {
		if specs[i].Fields == nil {
			specs[i].Fields = []connector.Field{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":            Version,
		"statuses":           domain.AllStatuses,
		"transitions":        transitions,
		"platforms":          domain.Platforms(),
		"connector_types":    specs,
		"agent_capabilities": crew.Capabilities(),
		"agent_roles":        store.AgentRoles,
		"task_statuses":      store.TaskStatuses,
		"task_priorities":    store.TaskPriorities,
	})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, err := s.reqOrg(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	byStatus, err := s.Store.CountPostsByStatus(ctx, org.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	ct, ca, pt, pa, err := s.Store.Counts(ctx, org.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var last *store.Run
	if runs, err := s.Store.ListRuns(ctx, &org.ID, nil, 1); err == nil && len(runs) > 0 {
		last = runs[0]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"posts_by_status": byStatus,
		"connectors":      map[string]int{"total": ct, "active": ca},
		"pipelines":       map[string]int{"total": pt, "active": pa},
		"last_run":        last,
	})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, cancel := s.Broker.Subscribe()
	defer cancel()
	fmt.Fprint(w, "retry: 3000\n: connected\n\n")
	flusher.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(ev.Data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, data)
			flusher.Flush()
		}
	}
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]string{"code": errCode, "message": msg}})
}

// fail maps domain/store errors to HTTP responses.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var te *domain.TransitionError
	var ve *store.ValidationError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, domain.ErrVersionConflict):
		writeError(w, http.StatusConflict, "version_conflict", "the post was changed by someone else; reload and try again")
	case errors.As(err, &te):
		writeError(w, http.StatusConflict, "invalid_transition", te.Error())
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "invalid_argument", ve.Error())
	case errors.Is(err, pipeline.ErrAlreadyRunning):
		writeError(w, http.StatusConflict, "already_running", err.Error())
	case errors.Is(err, context.Canceled):
		writeError(w, 499, "canceled", "request canceled")
	default:
		s.Logger.Error("request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	if err := dec.Decode(v); err != nil {
		return &store.ValidationError{Msg: "invalid JSON body: " + err.Error()}
	}
	return nil
}

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, domain.ErrNotFound
	}
	return id, nil
}

func queryInt(r *http.Request, key string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil {
		return v
	}
	return def
}

func queryUUID(r *http.Request, key string) (*uuid.UUID, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil, nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return nil, &store.ValidationError{Msg: key + " must be a UUID"}
	}
	return &id, nil
}
