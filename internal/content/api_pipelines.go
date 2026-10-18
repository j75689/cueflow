package content

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/api"
	"github.com/j75689/cueflow/internal/scheduler"
)

func (h *handlers) withNextRun(p *Pipeline) *Pipeline {
	p.NextRunAt = h.m.Scheduler.NextFire(KindPipeline, p.ID)
	return p
}

func (h *handlers) listPipelines(w http.ResponseWriter, r *http.Request) {
	org, err := h.s.ReqOrg(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	ps, err := h.m.Store.ListPipelines(r.Context(), &org.ID)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	for _, p := range ps {
		h.withNextRun(p)
	}
	api.WriteJSON(w, http.StatusOK, ps)
}

func (h *handlers) getPipeline(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	p, err := h.m.Store.GetPipeline(r.Context(), id)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, h.withNextRun(p))
}

// pipelineBody mirrors the editable Pipeline fields; decoding a PATCH onto the current row
// leaves absent fields unchanged.
type pipelineBody struct {
	Name                 *string        `json:"name"`
	Description          *string        `json:"description"`
	IsActive             *bool          `json:"is_active"`
	CronExpr             *string        `json:"cron_expr"`
	Timezone             *string        `json:"timezone"`
	Topic                *string        `json:"topic"`
	Instructions         *string        `json:"instructions"`
	Language             *string        `json:"language"`
	Platforms            *[]string      `json:"platforms"`
	LLMConnectorID       api.NullableID `json:"llm_connector_id"`
	SourceConnectorIDs   *[]uuid.UUID   `json:"source_connector_ids"`
	SearchConnectorID    api.NullableID `json:"search_connector_id"`
	SearchQueries        *[]string      `json:"search_queries"`
	PublisherConnectorID api.NullableID `json:"publisher_connector_id"`
	NotifierConnectorID  api.NullableID `json:"notifier_connector_id"`
	LookbackHours        *int           `json:"lookback_hours"`
	MaxItems             *int           `json:"max_items"`
}

func (b *pipelineBody) apply(p *Pipeline) {
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
		src api.NullableID
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

func (h *handlers) createPipeline(w http.ResponseWriter, r *http.Request) {
	var body pipelineBody
	if err := api.Decode(r, &body); err != nil {
		h.s.Fail(w, err)
		return
	}
	org, err := h.s.ReqOrg(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	p := &Pipeline{OrgID: org.ID, IsActive: true, Timezone: "UTC", Language: "en", LookbackHours: 24, MaxItems: 20}
	body.apply(p)
	out, err := h.m.Store.CreatePipeline(r.Context(), p, scheduler.Validate)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	_ = h.m.Scheduler.Reload(r.Context())
	api.WriteJSON(w, http.StatusCreated, h.withNextRun(out))
}

func (h *handlers) updatePipeline(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	var body pipelineBody
	if err := api.Decode(r, &body); err != nil {
		h.s.Fail(w, err)
		return
	}
	p, err := h.m.Store.GetPipeline(r.Context(), id)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	body.apply(p)
	out, err := h.m.Store.UpdatePipeline(r.Context(), p, scheduler.Validate)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	_ = h.m.Scheduler.Reload(r.Context())
	api.WriteJSON(w, http.StatusOK, h.withNextRun(out))
}

func (h *handlers) deletePipeline(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	if err := h.m.Store.DeletePipeline(r.Context(), id); err != nil {
		h.s.Fail(w, err)
		return
	}
	_ = h.m.Scheduler.Reload(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) runPipeline(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	if _, err := h.m.Store.GetPipeline(r.Context(), id); err != nil {
		h.s.Fail(w, err)
		return
	}
	runID, err := h.m.Runner.Trigger(r.Context(), id, "manual")
	if errors.Is(err, ErrAlreadyRunning) {
		api.WriteError(w, http.StatusConflict, "already_running", err.Error())
		return
	}
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"run_id": runID})
}

func (h *handlers) listRuns(w http.ResponseWriter, r *http.Request) {
	pid, err := api.QueryUUID(r, "pipeline_id")
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	org, err := h.s.ReqOrg(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	runs, err := h.m.Store.ListRuns(r.Context(), &org.ID, pid, api.QueryInt(r, "limit", 50))
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, runs)
}

func (h *handlers) getRun(w http.ResponseWriter, r *http.Request) {
	id, err := api.PathID(r)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	run, err := h.m.Store.GetRun(r.Context(), id)
	if err != nil {
		h.s.Fail(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, run)
}
