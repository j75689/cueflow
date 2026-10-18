// Package scheduler is the heartbeat: it opens the tasks of recurring task schedules, fires the
// cron rows modules register (CronSource) and runs their periodic jobs (AddPeriodic), in-process
// or, with an external clock, by claiming what is due and handing it to the queue.
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

	"github.com/j75689/cueflow/internal/jobs"
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

// CronEntry is one row a CronSource wants fired on a cron expression.
type CronEntry struct {
	ID       uuid.UUID
	Expr     string
	Timezone string
}

// CronSource is a table of rows with cron expressions a module wants fired (the content module's
// simple schedules). Kind names the work for the queue; Table is the row's table for ClaimDue.
type CronSource interface {
	Kind() jobs.Kind
	Table() string
	// Active lists the rows to register with the in-process cron.
	Active(ctx context.Context) ([]CronEntry, error)
	// Entry returns one row, or nil when it is inactive or gone (for NextFire with an external clock).
	Entry(ctx context.Context, id uuid.UUID) (*CronEntry, error)
	// Fire does the row's work inline.
	Fire(ctx context.Context, id uuid.UUID) error
}

type periodic struct {
	name, expr string
	fn         func(context.Context)
}

type Scheduler struct {
	store  *store.Store
	logger *slog.Logger

	// Queue, when set, receives due work (cloud deployment); otherwise Tick runs it inline.
	Queue jobs.Queue
	// External marks that an outside clock calls Tick; there is no in-process cron.
	External bool
	// MediaRetention, when positive, makes Tick delete media older than that which no unfinished
	// post uses.
	MediaRetention time.Duration

	// OnTaskSpawned is called after a schedule creates a task (main wires it to the crew).
	OnTaskSpawned func(ctx context.Context, t *store.Task)

	mu        sync.Mutex
	cron      *cron.Cron
	sources   []CronSource
	periodics []periodic
	entries   map[jobs.Kind]map[uuid.UUID]cron.EntryID // per source
	schedules map[uuid.UUID]cron.EntryID
}

func New(st *store.Store, logger *slog.Logger) *Scheduler {
	return &Scheduler{store: st, logger: logger.With("component", "scheduler"),
		entries: map[jobs.Kind]map[uuid.UUID]cron.EntryID{}, schedules: map[uuid.UUID]cron.EntryID{}}
}

// AddSource registers a module's cron rows. Call it before Start.
func (s *Scheduler) AddSource(src CronSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sources = append(s.sources, src)
	s.entries[src.Kind()] = map[uuid.UUID]cron.EntryID{}
}

// AddPeriodic registers a job that runs on a cron expression in the server's local time (a review
// digest, say). name identifies it in the clock table. Call it before Start.
func (s *Scheduler) AddPeriodic(name, expr string, fn func(context.Context)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.periodics = append(s.periodics, periodic{name: name, expr: expr, fn: fn})
}

func (s *Scheduler) source(kind jobs.Kind) CronSource {
	for _, src := range s.sources {
		if src.Kind() == kind {
			return src
		}
	}
	return nil
}

func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	s.cron = cron.New(cron.WithParser(parser), cron.WithChain(cron.Recover(cron.DefaultLogger)))
	s.mu.Unlock()
	for _, p := range s.periodics {
		fn := p.fn
		if _, err := s.cron.AddFunc(p.expr, func() { fn(ctx) }); err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
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

// Reload re-registers every active row of every source and every task schedule. Called at startup
// and after they change.
func (s *Scheduler) Reload(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron == nil {
		return nil
	}
	for _, src := range s.sources {
		rows, err := src.Active(ctx)
		if err != nil {
			return err
		}
		entries := s.entries[src.Kind()]
		for id, e := range entries {
			s.cron.Remove(e)
			delete(entries, id)
		}
		for _, row := range rows {
			id, src := row.ID, src
			e, err := s.cron.AddFunc(spec(row.Expr, row.Timezone), func() {
				if err := src.Fire(context.Background(), id); err != nil {
					s.logger.Warn("scheduled work failed", "kind", src.Kind(), "id", id, "err", err)
				}
			})
			if err != nil {
				s.logger.Error("bad cron expression", "kind", src.Kind(), "id", id, "expr", row.Expr, "err", err)
				continue
			}
			entries[id] = e
		}
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
	rows := 0
	for _, m := range s.entries {
		rows += len(m)
	}
	s.logger.Info("schedule loaded", "module_rows", rows, "task_schedules", len(s.schedules))
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

// NextFire reports when a source's row fires next, or nil when it is inactive.
func (s *Scheduler) NextFire(kind jobs.Kind, id uuid.UUID) *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron == nil {
		if src := s.source(kind); s.External && src != nil {
			if row, err := src.Entry(context.Background(), id); err == nil && row != nil {
				return nextAfterNow(row.Expr, row.Timezone)
			}
		}
		return nil
	}
	e, ok := s.entries[kind][id]
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

// Tick is the external clock's entry point (called every minute): it fires every task schedule,
// every source row and every periodic job whose time has come, exactly once even if several
// instances tick at once, and moves each to its next activation.
func (s *Scheduler) Tick(ctx context.Context) error {
	now := time.Now()
	schedules, err := s.store.ClaimDue(ctx, "schedules", now, tickGrace, Next)
	if err != nil {
		return fmt.Errorf("claim schedules: %w", err)
	}
	for _, id := range schedules {
		s.fire(ctx, jobs.KindSchedule, id)
	}
	for _, src := range s.sources {
		due, err := s.store.ClaimDue(ctx, src.Table(), now, tickGrace, Next)
		if err != nil {
			return fmt.Errorf("claim %s: %w", src.Table(), err)
		}
		for _, id := range due {
			s.fire(ctx, src.Kind(), id)
		}
	}
	for _, p := range s.periodics {
		due, err := s.store.ClaimClock(ctx, p.name, now, tickGrace, func(t time.Time) (time.Time, error) {
			return Next(p.expr, "Local", t)
		})
		if err != nil {
			return fmt.Errorf("claim %s: %w", p.name, err)
		}
		if due {
			p.fn(ctx)
		}
	}
	_ = s.store.PurgeRunTokens(ctx)
	if s.MediaRetention > 0 {
		if n, err := s.store.PruneMedia(ctx, s.MediaRetention, false); err != nil {
			s.logger.Warn("prune old media", "err", err)
		} else if n > 0 {
			s.logger.Info("deleted old media", "count", n)
		}
	}
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
	if kind == jobs.KindSchedule {
		s.spawn(ctx, id)
		return
	}
	if src := s.source(kind); src != nil {
		if err := src.Fire(ctx, id); err != nil {
			s.logger.Warn("scheduled work failed", "kind", kind, "id", id, "err", err)
		}
	}
}

// HandleSchedule is the jobs.KindSchedule handler: it creates the schedule's task.
func (s *Scheduler) HandleSchedule(ctx context.Context, id uuid.UUID) error {
	s.spawn(ctx, id)
	return nil
}
