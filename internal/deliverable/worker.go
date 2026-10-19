package deliverable

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/guard"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/jobs"
)

// KindDeliver is the queue kind for the worker (id is uuid.Nil: "whichever is next").
const KindDeliver jobs.Kind = "deliver"

// Worker is the only code path that calls an action connector, and only for items a human
// approved: APPROVED -> QUEUED -> (act) -> DONE | FAILED. An item whose org has no action
// connector is DONE as soon as it is approved: the review itself was the point.
type Worker struct {
	store   *Store
	hub     *hub.Hub
	broker  *events.Broker
	logger  *slog.Logger
	every   time.Duration
	lease   time.Duration
	timeout time.Duration
	wake    chan struct{}
	// Queue, when set (cloud deployment), replaces the in-process wake-up: Wake enqueues a
	// KindDeliver delivery, handled by Handle.
	Queue jobs.Queue
}

func NewWorker(st *Store, h *hub.Hub, b *events.Broker, logger *slog.Logger, every, lease, timeout time.Duration) *Worker {
	return &Worker{store: st, hub: h, broker: b, logger: logger.With("component", "deliverables"),
		every: every, lease: lease, timeout: timeout, wake: make(chan struct{}, 1)}
}

// Wake makes the worker run a cycle immediately (called right after an approval or retry).
func (w *Worker) Wake() {
	if w.Queue != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := w.Queue.Enqueue(ctx, KindDeliver, uuid.Nil, 0); err != nil {
			w.logger.Warn("enqueue deliverable work", "err", err)
		}
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.every)
	defer t.Stop()
	for {
		w.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.wake:
		}
	}
}

// Tick runs one cycle: fail expired leases, queue the approved, act on the queued.
func (w *Worker) Tick(ctx context.Context) {
	if err := w.failExpired(ctx); err != nil && ctx.Err() == nil {
		w.logger.Error("expire leases", "err", err)
	}
	queued, err := w.store.QueueApproved(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.logger.Error("queue approved", "err", err)
		}
		return
	}
	for _, d := range queued {
		w.broker.Publish(d.OrgID, "deliverable.updated", map[string]any{"id": d.ID, "status": StatusQueued})
	}
	for {
		items, err := w.store.ClaimQueued(ctx, w.lease, 5)
		if err != nil {
			if ctx.Err() == nil {
				w.logger.Error("claim queued", "err", err)
			}
			return
		}
		if len(items) == 0 {
			return
		}
		for _, d := range items {
			w.act(ctx, d)
		}
	}
}

// Handle is the KindDeliver handler: one pass of Tick.
func (w *Worker) Handle(ctx context.Context) error {
	w.Tick(ctx)
	return nil
}

// Sweep is the worker's part of the external clock.
func (w *Worker) Sweep(ctx context.Context) error {
	if err := w.failExpired(ctx); err != nil {
		return err
	}
	n, err := w.store.ActionableCount(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		w.Wake()
	}
	return nil
}

func (w *Worker) failExpired(ctx context.Context) error {
	ids, err := w.store.ExpiredLeases(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		msg := "worker stopped while acting; the action may or may not have happened - check before retrying"
		d, err := w.store.Transition(ctx, id, StatusFailed, ActionFailed, ActorWorker, msg, &TransitionOpts{ErrorMessage: &msg})
		if err != nil {
			w.logger.Error("fail expired lease", "deliverable", id, "err", err)
			continue
		}
		w.broker.Publish(d.OrgID, "deliverable.updated", map[string]any{"id": id, "status": StatusFailed})
	}
	return nil
}

// errNoAction means the org has no action connector: approval is the end of the road.
var errNoAction = errors.New("no action connector")

func (w *Worker) act(ctx context.Context, d *Deliverable) {
	log := w.logger.With("deliverable", d.ID)
	res, err := w.doAct(ctx, d)
	if ctx.Err() != nil {
		return // shutting down: the lease expires and failExpired handles it on the next start
	}
	now := time.Now().UTC()
	empty := ""
	if errors.Is(err, errNoAction) {
		if _, terr := w.store.Transition(context.Background(), d.ID, StatusDone, ActionDone, ActorWorker,
			"approved; no action connector configured, nothing to run", &TransitionOpts{ActedAt: &now, ErrorMessage: &empty}); terr != nil {
			log.Error("mark done", "err", terr)
		}
		w.broker.Publish(d.OrgID, "deliverable.updated", map[string]any{"id": d.ID, "status": StatusDone})
		return
	}
	if err != nil {
		msg := err.Error()
		log.Warn("action failed", "err", err)
		opts := &TransitionOpts{ErrorMessage: &msg}
		var partial *connector.PartialError
		if errors.As(err, &partial) {
			opts.ExternalIDs = partial.ExternalIDs
		}
		if _, terr := w.store.Transition(context.Background(), d.ID, StatusFailed, ActionFailed, ActorWorker, msg, opts); terr != nil {
			log.Error("mark failed", "err", terr)
		}
		w.broker.Publish(d.OrgID, "deliverable.updated", map[string]any{"id": d.ID, "status": StatusFailed})
		return
	}
	note := fmt.Sprintf("external ids: %v", res.ExternalIDs)
	if _, err := w.store.Transition(context.Background(), d.ID, StatusDone, ActionDone, ActorWorker, note,
		&TransitionOpts{ExternalIDs: res.ExternalIDs, Result: res.Details, ActedAt: &now, ErrorMessage: &empty}); err != nil {
		log.Error("mark done", "err", err)
		return
	}
	log.Info("done", "external_ids", res.ExternalIDs)
	w.broker.Publish(d.OrgID, "deliverable.updated", map[string]any{"id": d.ID, "status": StatusDone})
}

func (w *Worker) doAct(ctx context.Context, d *Deliverable) (*connector.ActResult, error) {
	if k, ok := KindByID(d.Kind); ok && k.Act != nil {
		_ = w.store.AddAudit(ctx, d.ID, ActionStarted, ActorWorker, "via the "+k.ID+" kind")
		actx, cancel := context.WithTimeout(ctx, w.timeout)
		defer cancel()
		res, err := k.Act(guard.WithPublishGrant(actx, d.ID), d)
		if err != nil {
			return nil, err
		}
		if res == nil {
			res = &connector.ActResult{}
		}
		if res.ExternalIDs == nil {
			res.ExternalIDs = map[string]string{}
		}
		if res.Details == nil {
			res.Details = map[string]any{}
		}
		return res, nil
	}
	actionID, err := w.resolveAction(ctx, d)
	if err != nil {
		return nil, err
	}
	actor, c, err := w.hub.Actor(ctx, actionID)
	if err != nil {
		return nil, err
	}
	defer hub.Close(actor)
	_ = w.store.AddAudit(ctx, d.ID, ActionStarted, ActorWorker, "via "+c.Name)

	actx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	actx = guard.WithPublishGrant(actx, d.ID)
	res, err := actor.Act(actx, connector.ActRequest{DeliverableID: d.ID, Kind: d.Kind, Title: d.Title, Payload: d.Payload, Previous: d.ExternalIDs})
	if err != nil {
		return nil, err
	}
	if res == nil {
		res = &connector.ActResult{}
	}
	if res.ExternalIDs == nil {
		res.ExternalIDs = map[string]string{}
	}
	if res.Details == nil {
		res.Details = map[string]any{}
	}
	return res, nil
}

func (w *Worker) resolveAction(ctx context.Context, d *Deliverable) (uuid.UUID, error) {
	if d.ActionConnectorID != nil {
		return *d.ActionConnectorID, nil
	}
	c, err := w.store.FirstActiveConnector(ctx, connector.KindAction)
	if errors.Is(err, domain.ErrNotFound) {
		return uuid.Nil, errNoAction
	}
	if err != nil {
		return uuid.Nil, err
	}
	return c.ID, nil
}
