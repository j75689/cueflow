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

	"github.com/j75689/cueflow/internal/cfaccess"
	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/jobs"
	"github.com/j75689/cueflow/internal/media"
	"github.com/j75689/cueflow/internal/scheduler"
	"github.com/j75689/cueflow/internal/store"
	"github.com/j75689/cueflow/internal/version"
	"github.com/j75689/cueflow/internal/webui"
)

type Server struct {
	Store      *store.Store
	Hub        *hub.Hub
	Broker     *events.Broker
	Scheduler  *scheduler.Scheduler
	Crew       *crew.Crew
	Logger     *slog.Logger
	AdminToken string
	// MCPToken enables the operator MCP endpoint (CUEFLOW_MCP_TOKEN); empty disables it.
	MCPToken string
	// PublicURL is how people reach this server (links in emails).
	PublicURL string
	// DirectURL is how machines reach it (runner registration commands, MCP setup commands); empty
	// means PublicURL. They differ when browsers go through a login gate such as Cloudflare Access.
	DirectURL string
	// Access, when set, lets requests through that carry a valid Cloudflare Access JWT, as an
	// alternative to the admin token.
	Access *cfaccess.Verifier

	// Cloud deployment: when InternalAuth is set, /internal/tick and /internal/work/{kind}/{id}
	// are served behind it (Cloud Scheduler and Cloud Tasks call them with Google ID tokens).
	InternalAuth func(http.Handler) http.Handler
	// MediaRegisterAuth guards POST /internal/media/register, which only the accounts of media
	// producers (e.g. a video renderer) may call (not the Cloud Tasks / Scheduler accounts InternalAuth lets in).
	MediaRegisterAuth func(http.Handler) http.Handler
	Handlers          jobs.Handlers
	Tick              func(context.Context) error
	// Modules are the feature sets mounted on top of the core (see Module); main sets them.
	Modules []Module
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

		r.Get("/connectors", s.listConnectors)
		r.Post("/connectors", s.createConnector)
		r.Get("/connectors/{id}", s.getConnector)
		r.Patch("/connectors/{id}", s.updateConnector)
		r.Delete("/connectors/{id}", s.deleteConnector)
		r.Post("/connectors/{id}/test", s.testConnector)
		r.Get("/connectors/{id}/tools", s.connectorTools)
		r.Get("/connectors/{id}/tool-permissions", s.toolPermissions)
		r.Put("/connectors/{id}/tool-permissions", s.setToolPermissions)

		r.Get("/agents", s.listAgents)
		r.Post("/agents", s.createAgent)
		r.Get("/agents/template", s.exportTeam)
		r.Post("/agents/template", s.importTeam)
		r.Get("/agents/{id}", s.getAgent)
		r.Patch("/agents/{id}", s.updateAgent)
		r.Delete("/agents/{id}", s.deleteAgent)
		r.Get("/agents/{id}/memories", s.listMemories)
		r.Post("/agents/{id}/memories", s.addMemory)
		r.Delete("/agents/{id}/memories/{memoryId}", s.deleteMemory)
		r.Get("/agents/{id}/tokens", s.listAgentTokens)
		r.Post("/agents/{id}/tokens", s.createAgentToken)
		r.Delete("/agents/{id}/tokens/{tokenId}", s.revokeAgentToken)

		r.Get("/tasks", s.listTasks)
		r.Post("/tasks", s.createTask)
		r.Post("/tasks/bulk", s.bulkTasks)
		r.Get("/tasks/{id}", s.getTask)
		r.Patch("/tasks/{id}", s.updateTask)
		r.Delete("/tasks/{id}", s.deleteTask)
		r.Get("/tasks/{id}/messages", s.taskMessages)
		r.Post("/tasks/{id}/messages", s.postTaskMessage)
		r.Post("/tasks/{id}/run", s.runTask)
		r.Post("/tasks/{id}/cancel", s.cancelTask)
		r.Get("/tasks/{id}/runs", s.taskRuns)
		r.Get("/tasks/{id}/files", s.listTaskFiles)
		r.Get("/tasks/{id}/files/content", s.taskFileContent)
		r.Get("/tasks/{id}/files/download", s.downloadTaskFile)
		r.Delete("/tasks/{id}/files", s.deleteTaskFile)
		r.Get("/tasks/{id}/approvals", s.taskApprovals)
		r.Post("/tasks/{id}/approvals/{approvalId}", s.decideApproval)

		r.Get("/tasks/{id}/runner-jobs", s.taskRunnerJobs)

		r.Get("/runners", s.listRunners)
		r.Post("/runners/registrations", s.createRegistration)
		r.Patch("/runners/{id}", s.updateRunner)
		r.Delete("/runners/{id}", s.deleteRunner)

		r.Get("/usage", s.usage)

		r.Post("/media", s.uploadMedia)
		r.Post("/media/generate", s.generateMedia)
		r.Delete("/media/{id}", s.deleteMedia)

		r.Get("/schedules", s.listSchedules)
		r.Post("/schedules", s.createSchedule)
		r.Get("/schedules/{id}", s.getSchedule)
		r.Patch("/schedules/{id}", s.updateSchedule)
		r.Delete("/schedules/{id}", s.deleteSchedule)
		r.Post("/schedules/{id}/run", s.runSchedule)

		for _, m := range s.Modules {
			m.Routes(s, r)
		}

		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			WriteError(w, http.StatusNotFound, "not_found", "no such endpoint")
		})
	})
	if s.InternalAuth != nil || s.MediaRegisterAuth != nil {
		r.Route("/internal", s.internalRoutes)
	}
	// Runners authenticate with their own tokens, not the admin token.
	r.Route("/api/runner/v1", s.runnerRoutes)
	// Per-run MCP tool servers for external (ACP) agents. Authenticated by the unguessable run
	// token in the path, not the admin token: the agent process never sees admin credentials.
	r.Handle(crew.MCPPathPrefix+"*", s.Crew.MCPHandler())
	// Operator MCP endpoint for external agents, authenticated by CUEFLOW_MCP_TOKEN.
	r.Handle(MCPPath, s.mcpHandler())
	// Stored images, public by unguessable id so publishers can fetch them.
	r.Get(media.PathPrefix+"{file}", s.serveMedia)
	r.Handle("/*", s.uiGuard(webui.Handler()))
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

// auth enforces the admin token (CUEFLOW_ADMIN_TOKEN) and/or Cloudflare Access login when
// configured. Either is enough: browsers log in through Access, scripts send the token.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (s.AdminToken == "" && s.Access == nil) || r.URL.Path == "/api/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		if s.Access != nil {
			email, err := s.Access.Verify(r)
			if err == nil {
				s.Logger.Debug("cloudflare access login", "user", email, "path", r.URL.Path)
				next.ServeHTTP(w, r)
				return
			}
			if !errors.Is(err, cfaccess.ErrNoToken) {
				s.Logger.Warn("rejected Cloudflare Access token", "err", err)
			}
		}
		if s.AdminToken != "" {
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if tok == "" && r.URL.Path == "/api/v1/events" {
				tok = r.URL.Query().Get("token")
			}
			if subtle.ConstantTimeCompare([]byte(tok), []byte(s.AdminToken)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		msg := "missing or invalid admin token"
		if s.Access != nil {
			msg = "sign in through Cloudflare Access, or send the admin token"
		}
		WriteError(w, http.StatusUnauthorized, "unauthorized", msg)
	})
}

// uiGuard keeps the dashboard's pages behind Cloudflare Access when it is configured. The service's
// own run.app address does not pass through Access, so without this anyone could load the pages
// there. They hold no data (the API needs the token or a login), but there is no reason to show them.
func (s *Server) uiGuard(next http.Handler) http.Handler {
	if s.Access == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.Access.Verify(r); err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprintf(w, "Sign in through Cloudflare Access: %s\n", s.PublicURL)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// machineURL is the address runners and MCP clients should use.
func (s *Server) machineURL() string {
	if s.DirectURL != "" {
		return s.DirectURL
	}
	return s.PublicURL
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
	WriteJSON(w, code, map[string]any{"status": "ok", "db": dbStatus, "time": time.Now().UTC()})
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	specs := connector.Specs()
	for i := range specs {
		if specs[i].Fields == nil {
			specs[i].Fields = []connector.Field{}
		}
	}
	out := map[string]any{
		"version":            version.Version,
		"modules":            s.moduleIDs(),
		"connector_types":    specs,
		"agent_capabilities": crew.Capabilities(),
		"agent_roles":        store.AgentRoles,
		"task_statuses":      store.TaskStatuses,
		"task_priorities":    store.TaskPriorities,
	}
	if s.Crew != nil {
		// How full a run's context may get before it hands off (CUEFLOW_CONTEXT_HANDOFF); the task page
		// colours the size from here.
		out["context_handoff_at"] = s.Crew.ContextHandOffAt
	}
	for _, m := range s.Modules {
		if p, ok := m.(MetaProvider); ok {
			p.Meta(s, out)
		}
	}
	WriteJSON(w, http.StatusOK, out)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	org, err := s.ReqOrg(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	ct, ca, err := s.Store.CountConnectors(ctx)
	if err != nil {
		s.Fail(w, err)
		return
	}
	out := map[string]any{"connectors": map[string]int{"total": ct, "active": ca}}
	for _, m := range s.Modules {
		if p, ok := m.(StatsProvider); ok {
			if err := p.Stats(ctx, s, org.ID, out); err != nil {
				s.Fail(w, err)
				return
			}
		}
	}
	WriteJSON(w, http.StatusOK, out)
}

// events streams live updates (SSE). With ?org= (id or slug) it sends that org's events and the global ones
// only, so a tab does not reload for other orgs' changes; without it, every event. An org that no longer exists
// (deleted while a tab watched it) gets the global events only, not an error: EventSource would retry the same
// URL forever, and the org.updated it needs to pick another org is a global event.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	watch := ""
	if r.URL.Query().Get(OrgParam) != "" {
		watch = "-" // matches no org's events
		if o, err := s.ReqOrg(r); err == nil {
			watch = o.ID.String()
		}
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
			if !ev.For(watch) {
				continue
			}
			data, _ := json.Marshal(ev.Data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, data)
			flusher.Flush()
		}
	}
}

// --- helpers ---

func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, code int, errCode, msg string) {
	WriteJSON(w, code, map[string]any{"error": map[string]string{"code": errCode, "message": msg}})
}

// Fail maps domain/store errors to HTTP responses.
func (s *Server) Fail(w http.ResponseWriter, err error) {
	status, code, msg := ErrorStatus(err)
	if status == http.StatusInternalServerError {
		s.Logger.Error("request failed", "err", err)
	}
	WriteError(w, status, code, msg)
}

// ErrorStatus is the HTTP status, error code and message a domain/store error maps to, for
// handlers that report errors per item instead of failing the request (Fail uses it too).
func ErrorStatus(err error) (status int, code, msg string) {
	var te *domain.TransitionError
	var ve *store.ValidationError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "not_found", "not found"
	case errors.Is(err, store.ErrMediaInUse):
		return http.StatusConflict, "in_use", "a deliverable that is not yet done or rejected still uses this media; remove it from the deliverable first"
	case errors.Is(err, domain.ErrVersionConflict):
		return http.StatusConflict, "version_conflict", "it was changed by someone else; reload and try again"
	case errors.As(err, &te):
		return http.StatusConflict, "invalid_transition", te.Error()
	case errors.As(err, &ve):
		return http.StatusBadRequest, "invalid_argument", ve.Error()
	case errors.Is(err, context.Canceled):
		return 499, "canceled", "request canceled"
	default:
		return http.StatusInternalServerError, "internal", err.Error()
	}
}

func Decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	if err := dec.Decode(v); err != nil {
		return &store.ValidationError{Msg: "invalid JSON body: " + err.Error()}
	}
	return nil
}

func PathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, domain.ErrNotFound
	}
	return id, nil
}

func QueryInt(r *http.Request, key string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil {
		return v
	}
	return def
}

func QueryUUID(r *http.Request, key string) (*uuid.UUID, error) {
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

// Optional distinguishes an absent JSON field from an explicit null.
type Optional[T any] struct {
	Set   bool
	Value *T
}

func (o *Optional[T]) UnmarshalJSON(b []byte) error {
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

// NullableID accepts a UUID, null, or "" (treated as null, which is what HTML selects send).
type NullableID struct {
	Set   bool
	Value *uuid.UUID
}

func (n *NullableID) UnmarshalJSON(b []byte) error {
	n.Set = true
	var s *string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == nil || *s == "" {
		n.Value = nil
		return nil
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return &store.ValidationError{Msg: "invalid connector id " + *s}
	}
	n.Value = &id
	return nil
}
