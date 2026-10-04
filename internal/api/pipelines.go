package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/scheduler"
	"github.com/j75689/cueflow/internal/store"
)

func (s *Server) withNextRun(p *store.Pipeline) *store.Pipeline {
	p.NextRunAt = s.Scheduler.NextRun(p.ID)
	return p
}

func (s *Server) listPipelines(w http.ResponseWriter, r *http.Request) {
	ps, err := s.Store.ListPipelines(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, p := range ps {
		s.withNextRun(p)
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) getPipeline(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	p, err := s.Store.GetPipeline(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.withNextRun(p))
}

// pipelineBody mirrors the editable Pipeline fields; decoding a PATCH onto the current row
// leaves absent fields unchanged.
type pipelineBody struct {
	Name                 *string      `json:"name"`
	Description          *string      `json:"description"`
	IsActive             *bool        `json:"is_active"`
	CronExpr             *string      `json:"cron_expr"`
	Timezone             *string      `json:"timezone"`
	Topic                *string      `json:"topic"`
	Instructions         *string      `json:"instructions"`
	Language             *string      `json:"language"`
	Platforms            *[]string    `json:"platforms"`
	LLMConnectorID       nullableID   `json:"llm_connector_id"`
	SourceConnectorIDs   *[]uuid.UUID `json:"source_connector_ids"`
	SearchConnectorID    nullableID   `json:"search_connector_id"`
	SearchQueries        *[]string    `json:"search_queries"`
	PublisherConnectorID nullableID   `json:"publisher_connector_id"`
	NotifierConnectorID  nullableID   `json:"notifier_connector_id"`
	LookbackHours        *int         `json:"lookback_hours"`
	MaxItems             *int         `json:"max_items"`
}

// nullableID accepts a UUID, null, or "" (treated as null, which is what HTML selects send).
type nullableID struct {
	Set   bool
	Value *uuid.UUID
}

func (n *nullableID) UnmarshalJSON(b []byte) error {
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

func (b *pipelineBody) apply(p *store.Pipeline) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&p.Name, b.Name)
	set(&p.Description, b.Description)
	set(&p.CronExpr, b.CronExpr)
	set(&p.Timezone, b.Timezone)
	set(&p.Topic, b.Topic)
	set(&p.Instructions, b.Instructions)
	set(&p.Language, b.Language)
	if b.IsActive != nil {
		p.IsActive = *b.IsActive
	}
	if b.Platforms != nil {
		p.Platforms = *b.Platforms
	}
	if b.SourceConnectorIDs != nil {
		p.SourceConnectorIDs = *b.SourceConnectorIDs
	}
	if b.SearchQueries != nil {
		p.SearchQueries = *b.SearchQueries
	}
	if b.LookbackHours != nil {
		p.LookbackHours = *b.LookbackHours
	}
	if b.MaxItems != nil {
		p.MaxItems = *b.MaxItems
	}
	for _, f := range []struct {
		src nullableID
		dst **uuid.UUID
	}{
		{b.LLMConnectorID, &p.LLMConnectorID}, {b.SearchConnectorID, &p.SearchConnectorID},
		{b.PublisherConnectorID, &p.PublisherConnectorID}, {b.NotifierConnectorID, &p.NotifierConnectorID},
	} {
		if f.src.Set {
			*f.dst = f.src.Value
		}
	}
}

func (s *Server) createPipeline(w http.ResponseWriter, r *http.Request) {
	var body pipelineBody
	if err := decode(r, &body); err != nil {
		s.fail(w, err)
		return
	}
	p := &store.Pipeline{IsActive: true, Timezone: "UTC", Language: "zh-TW", LookbackHours: 24, MaxItems: 20}
	body.apply(p)
	out, err := s.Store.CreatePipeline(r.Context(), p, scheduler.Validate)
	if err != nil {
		s.fail(w, err)
		return
	}
	_ = s.Scheduler.Reload(r.Context())
	writeJSON(w, http.StatusCreated, s.withNextRun(out))
}

func (s *Server) updatePipeline(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	var body pipelineBody
	if err := decode(r, &body); err != nil {
		s.fail(w, err)
		return
	}
	p, err := s.Store.GetPipeline(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	body.apply(p)
	out, err := s.Store.UpdatePipeline(r.Context(), p, scheduler.Validate)
	if err != nil {
		s.fail(w, err)
		return
	}
	_ = s.Scheduler.Reload(r.Context())
	writeJSON(w, http.StatusOK, s.withNextRun(out))
}

func (s *Server) deletePipeline(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.DeletePipeline(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	_ = s.Scheduler.Reload(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runPipeline(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, err := s.Store.GetPipeline(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	runID, err := s.Runner.Trigger(r.Context(), id, "manual")
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID})
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	pid, err := queryUUID(r, "pipeline_id")
	if err != nil {
		s.fail(w, err)
		return
	}
	runs, err := s.Store.ListRuns(r.Context(), pid, queryInt(r, "limit", 50))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	run, err := s.Store.GetRun(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}
