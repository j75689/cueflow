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

	mu      sync.Mutex
	cron    *cron.Cron
	entries map[uuid.UUID]cron.EntryID
}

func New(st *store.Store, r *pipeline.Runner, h *hub.Hub, logger *slog.Logger, digestCron, publicURL string) *Scheduler {
	return &Scheduler{store: st, runner: r, hub: h, logger: logger.With("component", "scheduler"),
		digest: digestCron, publicURL: publicURL, entries: map[uuid.UUID]cron.EntryID{}}
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
	pipelines, err := s.store.ListPipelines(ctx)
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
	s.logger.Info("schedule loaded", "pipelines", len(s.entries))
	return nil
}

// NextRun reports when an active pipeline fires next.
func (s *Scheduler) NextRun(id uuid.UUID) *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron == nil {
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
