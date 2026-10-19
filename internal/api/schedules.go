package api

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/scheduler"
	"github.com/j75689/cueflow/internal/store"
)

func (s *Server) withScheduleNext(sc *store.Schedule) *store.Schedule {
	sc.NextRunAt = s.Scheduler.ScheduleNextRun(sc.ID)
	return sc
}

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	org, err := s.ReqOrg(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	scs, err := s.Store.ListSchedules(r.Context(), &org.ID)
	if err != nil {
		s.Fail(w, err)
		return
	}
	for _, sc := range scs {
		s.withScheduleNext(sc)
	}
	WriteJSON(w, http.StatusOK, scs)
}

func (s *Server) getSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	sc, err := s.Store.GetSchedule(r.Context(), id)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, s.withScheduleNext(sc))
}

type scheduleBody struct {
	Name            *string    `json:"name"`
	CronExpr        *string    `json:"cron_expr"`
	Timezone        *string    `json:"timezone"`
	IsActive        *bool      `json:"is_active"`
	AssigneeAgentID NullableID `json:"assignee_agent_id"`
	TaskTitle       *string    `json:"task_title"`
	TaskDescription *string    `json:"task_description"`
	Priority        *string    `json:"priority"`
	// Publishing defaults copied to the tasks this schedule creates.
	PublisherConnectorID NullableID `json:"publisher_connector_id"`
	PublishAccounts      *[]string  `json:"publish_accounts"`
}

func (b *scheduleBody) apply(sc *store.Schedule) {
	for _, f := range []struct {
		dst *string
		src *string
	}{{&sc.Name, b.Name}, {&sc.CronExpr, b.CronExpr}, {&sc.Timezone, b.Timezone}, {&sc.TaskTitle, b.TaskTitle},
		{&sc.TaskDescription, b.TaskDescription}, {&sc.Priority, b.Priority}} {
		if f.src != nil {
			*f.dst = *f.src
		}
	}
	if b.IsActive != nil {
		sc.IsActive = *b.IsActive
	}
	if b.AssigneeAgentID.Set {
		sc.AssigneeAgentID = b.AssigneeAgentID.Value
	}
	if b.PublisherConnectorID.Set {
		sc.PublisherConnectorID = b.PublisherConnectorID.Value
	}
	if b.PublishAccounts != nil {
		sc.PublishAccounts = *b.PublishAccounts
	}
}

func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) {
	var body scheduleBody
	if err := Decode(r, &body); err != nil {
		s.Fail(w, err)
		return
	}
	org, err := s.ReqOrg(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	sc := &store.Schedule{OrgID: org.ID, IsActive: true, Timezone: "UTC", Priority: "normal", CreatedBy: "human"}
	body.apply(sc)
	out, err := s.Store.CreateSchedule(r.Context(), sc, scheduler.ValidateSchedule)
	if err != nil {
		s.Fail(w, err)
		return
	}
	_ = s.Scheduler.Reload(r.Context())
	s.Broker.Publish(out.OrgID, "schedule.updated", map[string]any{"id": out.ID})
	WriteJSON(w, http.StatusCreated, s.withScheduleNext(out))
}

func (s *Server) updateSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	var body scheduleBody
	if err := Decode(r, &body); err != nil {
		s.Fail(w, err)
		return
	}
	sc, err := s.Store.GetSchedule(r.Context(), id)
	if err != nil {
		s.Fail(w, err)
		return
	}
	body.apply(sc)
	out, err := s.Store.UpdateSchedule(r.Context(), sc, scheduler.ValidateSchedule)
	if err != nil {
		s.Fail(w, err)
		return
	}
	_ = s.Scheduler.Reload(r.Context())
	s.Broker.Publish(out.OrgID, "schedule.updated", map[string]any{"id": out.ID})
	WriteJSON(w, http.StatusOK, s.withScheduleNext(out))
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	org := uuid.Nil
	if sc, err := s.Store.GetSchedule(r.Context(), id); err == nil {
		org = sc.OrgID
	}
	if err := s.Store.DeleteSchedule(r.Context(), id); err != nil {
		s.Fail(w, err)
		return
	}
	_ = s.Scheduler.Reload(r.Context())
	s.Broker.Publish(org, "schedule.updated", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return
	}
	t, err := s.Scheduler.SpawnNow(r.Context(), id)
	if err != nil {
		s.Fail(w, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, map[string]any{"task_id": t.ID})
}
