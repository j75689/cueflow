// Package scheduler is the heartbeat: it triggers pipelines on their cron expressions and sends the
// optional review digest.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/jobs"
	"github.com/j75689/cueflow/internal/pipeline"
	"github.com/j75689/cueflow/internal/store"
)

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

func spec(expr, tz string) string {
	if tz == "" {
		tz = "UTC"
	}
	return "CRON_TZ=" + tz + " " + strings.TrimSpace(expr)
}

// Validate checks a 5-field cron expression or descriptor (@daily, @every 1h) in a timezone.
func Validate(expr, tz string) error {
	if strings.TrimSpace(expr) == "" {
		return errors.New("empty expression")
	}
	_, err := parser.Parse(spec(expr, tz))
	return err
}

// ValidateSchedule is Validate plus a minimum interval of 5 minutes between activations, which
// keeps an agent-created schedule from spawning a flood of tasks (and LLM spend).
func ValidateSchedule(expr, tz string) error {
	if err := Validate(expr, tz); err != nil {
		return err
	}
	s, _ := parser.Parse(spec(expr, tz))
	t := time.Now()
	for i := 0; i < 5; i++ {
		n := s.Next(t)
		if n.IsZero() {
			return nil
		}
		if i > 0 && n.Sub(t) < 5*time.Minute {
			return errors.New("runs more often than every 5 minutes")
		}
		t = n
	}
	return nil
}

// Next returns the next activation after t.
func Next(expr, tz string, t time.Time) (time.Time, error) {
	s, err := parser.Parse(spec(expr, tz))
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(t), nil
}

type Scheduler struct {
	store     *store.Store
	runner    *pipeline.Runner
	hub       *hub.Hub
	logger    *slog.Logger
	digest    string
	publicURL string

	// Queue, when set, receives due work (cloud deployment); otherwise Tick runs it inline.
	Queue jobs.Queue
	// External marks that an outside clock calls Tick; there is no in-process cron.
	External bool

	// OnTaskSpawned is called after a schedule creates a task (main wires it to the crew).
	OnTaskSpawned func(ctx context.Context, t *store.Task)

	mu        sync.Mutex
	cron      *cron.Cron
	entries   map[uuid.UUID]cron.EntryID
	schedules map[uuid.UUID]cron.EntryID
}

func New(st *store.Store, r *pipeline.Runner, h *hub.Hub, logger *slog.Logger, digestCron, publicURL string) *Scheduler {
	return &Scheduler{store: st, runner: r, hub: h, logger: logger.With("component", "scheduler"),
		digest: digestCron, publicURL: publicURL, entries: map[uuid.UUID]cron.EntryID{},
		schedules: map[uuid.UUID]cron.EntryID{}}
}

func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	s.cron = cron.New(cron.WithParser(parser), cron.WithChain(cron.Recover(cron.DefaultLogger)))
	s.mu.Unlock()
	if s.digest != "" {
		if _, err := s.cron.AddFunc(s.digest, func() { s.sendDigest(ctx) }); err != nil {
			return fmt.Errorf("CUEFLOW_DIGEST_CRON: %w", err)
		}
	}
	if err := s.Reload(ctx); err != nil {
		return err
	}
	s.cron.Start()
	go func() {
		<-ctx.Done()
		<-s.cron.Stop().Done()
	}()
	return nil
}

// Reload re-registers every active pipeline. Called at startup and after pipeline changes.
func (s *Scheduler) Reload(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron == nil {
		return nil
	}
	pipelines, err := s.store.ListPipelines(ctx, nil)
	if err != nil {
		return err
	}
	for id, e := range s.entries {
		s.cron.Remove(e)
		delete(s.entries, id)
	}
	for _, p := range pipelines {
		if !p.IsActive {
			continue
		}
		id := p.ID
		e, err := s.cron.AddFunc(spec(p.CronExpr, p.Timezone), func() {
			if _, err := s.runner.Trigger(context.Background(), id, "cron"); err != nil {
				s.logger.Warn("scheduled run not started", "pipeline", id, "err", err)
			}
		})
		if err != nil {
			s.logger.Error("bad cron expression", "pipeline", p.Name, "expr", p.CronExpr, "err", err)
			continue
		}
		s.entries[id] = e
	}
	for id, e := range s.schedules {
		s.cron.Remove(e)
		delete(s.schedules, id)
	}
	schedules, err := s.store.ListSchedules(ctx, nil)
	if err != nil {
		return err
	}
	for _, sc := range schedules {
		if !sc.IsActive {
			continue
		}
		id := sc.ID
		e, err := s.cron.AddFunc(spec(sc.CronExpr, sc.Timezone), func() { s.spawn(context.Background(), id) })
		if err != nil {
			s.logger.Error("bad schedule", "schedule", sc.Name, "expr", sc.CronExpr, "err", err)
			continue
		}
		s.schedules[id] = e
	}
	s.logger.Info("schedule loaded", "pipelines", len(s.entries), "task_schedules", len(s.schedules))
	return nil
}

func (s *Scheduler) spawn(ctx context.Context, scheduleID uuid.UUID) {
	t, err := s.store.SpawnScheduledTask(ctx, scheduleID)
	if err != nil {
		s.logger.Warn("scheduled task not created", "schedule", scheduleID, "err", err)
		return
	}
	if s.OnTaskSpawned != nil {
		s.OnTaskSpawned(ctx, t)
	}
}

// SpawnNow creates a schedule's task immediately ("Run now").
func (s *Scheduler) SpawnNow(ctx context.Context, scheduleID uuid.UUID) (*store.Task, error) {
	t, err := s.store.SpawnScheduledTask(ctx, scheduleID)
	if err != nil {
		return nil, err
	}
	if s.OnTaskSpawned != nil {
		s.OnTaskSpawned(ctx, t)
	}
	return t, nil
}

// ScheduleNextRun reports when a task schedule fires next.
func (s *Scheduler) ScheduleNextRun(id uuid.UUID) *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron == nil {
		if sc, err := s.store.GetSchedule(context.Background(), id); s.External && err == nil && sc.IsActive {
			return nextAfterNow(sc.CronExpr, sc.Timezone)
		}
		return nil
	}
	e, ok := s.schedules[id]
	if !ok {
		return nil
	}
	next := s.cron.Entry(e).Next
	if next.IsZero() {
		return nil
	}
	return &next
}

// NextRun reports when an active pipeline fires next.
func (s *Scheduler) NextRun(id uuid.UUID) *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron == nil {
		if p, err := s.store.GetPipeline(context.Background(), id); s.External && err == nil && p.IsActive {
			return nextAfterNow(p.CronExpr, p.Timezone)
		}
		return nil
	}
	e, ok := s.entries[id]
	if !ok {
		return nil
	}
	next := s.cron.Entry(e).Next
	if next.IsZero() {
		return nil
	}
	return &next
}

func nextAfterNow(expr, tz string) *time.Time {
	n, err := Next(expr, tz, time.Now())
	if err != nil || n.IsZero() {
		return nil
	}
	return &n
}

// tickGrace is how late an activation may be and still fire; older ones were missed while the
// clock was down and are skipped rather than run at a surprising hour.
const tickGrace = 15 * time.Minute

// Tick is the external clock's entry point (called every minute): it fires every schedule,
// pipeline and the review digest whose time has come, exactly once even if several instances tick
// at once, and moves each to its next activation.
func (s *Scheduler) Tick(ctx context.Context) error {
	now := time.Now()
	schedules, err := s.store.ClaimDue(ctx, "schedules", now, tickGrace, Next)
	if err != nil {
		return fmt.Errorf("claim schedules: %w", err)
	}
	for _, id := range schedules {
		s.fire(ctx, jobs.KindSchedule, id)
	}
	pipelines, err := s.store.ClaimDue(ctx, "pipelines", now, tickGrace, Next)
	if err != nil {
		return fmt.Errorf("claim pipelines: %w", err)
	}
	for _, id := range pipelines {
		s.fire(ctx, jobs.KindPipeline, id)
	}
	if s.digest != "" {
		due, err := s.store.ClaimClock(ctx, "digest", now, tickGrace, func(t time.Time) (time.Time, error) {
			return Next(s.digest, "Local", t)
		})
		if err != nil {
			return fmt.Errorf("claim digest: %w", err)
		}
		if due {
			s.sendDigest(ctx)
		}
	}
	_ = s.store.PurgeRunTokens(ctx)
	return nil
}

// fire hands due work to the queue, or does it inline when there is none.
func (s *Scheduler) fire(ctx context.Context, kind jobs.Kind, id uuid.UUID) {
	if s.Queue != nil {
		if err := s.Queue.Enqueue(ctx, kind, id, 0); err != nil {
			s.logger.Error("enqueue scheduled work", "kind", kind, "id", id, "err", err)
		}
		return
	}
	var err error
	switch kind {
	case jobs.KindSchedule:
		s.spawn(ctx, id)
	case jobs.KindPipeline:
		err = s.runner.RunSync(ctx, id, "cron")
	}
	if err != nil {
		s.logger.Warn("scheduled run not started", "kind", kind, "id", id, "err", err)
	}
}

// HandleSchedule is the jobs.KindSchedule handler: it creates the schedule's task.
func (s *Scheduler) HandleSchedule(ctx context.Context, id uuid.UUID) error {
	s.spawn(ctx, id)
	return nil
}

// HandlePipeline is the jobs.KindPipeline handler: it runs the pipeline to completion.
func (s *Scheduler) HandlePipeline(ctx context.Context, id uuid.UUID) error {
	return s.runner.RunSync(ctx, id, "cron")
}

func (s *Scheduler) sendDigest(ctx context.Context) {
	posts, err := s.store.PendingReview(ctx, 50)
	if err != nil || len(posts) == 0 {
		return
	}
	notifiers, err := s.store.ListConnectors(ctx, string(connector.KindNotifier))
	if err != nil {
		return
	}
	note := pipeline.ReviewDigest(strings.TrimRight(s.publicURL, "/"),
		fmt.Sprintf("[CueFlow] %d drafts awaiting review", len(posts)), posts)
	for _, c := range notifiers {
		if !c.IsActive {
			continue
		}
		n, _, err := s.hub.Notifier(ctx, c.ID)
		if err != nil {
			s.logger.Warn("digest notifier", "connector", c.Name, "err", err)
			continue
		}
		if err := n.Notify(ctx, note); err != nil {
			s.logger.Warn("digest send", "connector", c.Name, "err", err)
		}
		hub.Close(n)
	}
}
