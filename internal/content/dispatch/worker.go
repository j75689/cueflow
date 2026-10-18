// Package dispatch is the publish worker. It is the only code path that calls a publisher, and only
// for posts a human approved: APPROVED -> QUEUED -> (publish) -> PUBLISHED | FAILED.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/content"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/guard"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/jobs"
)

type Worker struct {
	store   *content.Store
	hub     *hub.Hub
	broker  *events.Broker
	logger  *slog.Logger
	every   time.Duration
	lease   time.Duration
	timeout time.Duration
	wake    chan struct{}
	// Queue, when set (cloud deployment), replaces the in-process wake-up: Wake enqueues a
	// content.KindDispatch delivery, handled by Handle.
	Queue jobs.Queue
	// MediaBaseURL turns stored media ("/media/{id}") into URLs publishers can fetch.
	MediaBaseURL string
}

func NewWorker(st *content.Store, h *hub.Hub, b *events.Broker, logger *slog.Logger, every, lease, timeout time.Duration) *Worker {
	return &Worker{store: st, hub: h, broker: b, logger: logger.With("component", "dispatch"),
		every: every, lease: lease, timeout: timeout, wake: make(chan struct{}, 1)}
}

// Wake makes the worker run a cycle immediately (called right after an approval or retry).
func (w *Worker) Wake() {
	if w.Queue != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := w.Queue.Enqueue(ctx, content.KindDispatch, uuid.Nil, 0); err != nil {
			w.logger.Warn("enqueue publish work", "err", err)
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

// Tick runs one dispatch cycle.
func (w *Worker) Tick(ctx context.Context) {
	if err := w.failExpired(ctx); err != nil && ctx.Err() == nil {
		w.logger.Error("expire leases", "err", err)
	}
	ids, err := w.store.QueueApproved(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.logger.Error("queue approved", "err", err)
		}
		return
	}
	for _, id := range ids {
		w.broker.Publish("post.updated", map[string]any{"id": id, "status": content.StatusQueued})
	}
	for {
		posts, err := w.store.ClaimQueued(ctx, w.lease, 5)
		if err != nil {
			if ctx.Err() == nil {
				w.logger.Error("claim queued", "err", err)
			}
			return
		}
		if len(posts) == 0 {
			return
		}
		for _, p := range posts {
			w.publish(ctx, p)
		}
	}
}

// Handle is the content.KindDispatch handler: one pass of Tick. Tick already drains every queued post,
// and claiming is atomic, so duplicate deliveries are harmless.
func (w *Worker) Handle(ctx context.Context) error {
	w.Tick(ctx)
	return nil
}

// Sweep is the publisher's part of the external clock: it fails posts whose publisher died mid-call
// and makes sure waiting posts have a delivery coming.
func (w *Worker) Sweep(ctx context.Context) error {
	if err := w.failExpired(ctx); err != nil {
		return err
	}
	n, err := w.store.PublishableCount(ctx)
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
		msg := "worker stopped while publishing; the post may or may not have been published - check the platform before retrying"
		if _, err := w.store.Transition(ctx, id, content.StatusFailed, content.ActionFailed, content.ActorWorker, msg,
			&content.TransitionOpts{ErrorMessage: &msg}); err != nil {
			w.logger.Error("fail expired lease", "post", id, "err", err)
			continue
		}
		w.broker.Publish("post.updated", map[string]any{"id": id, "status": content.StatusFailed})
	}
	return nil
}

func (w *Worker) publish(ctx context.Context, p *content.Post) {
	log := w.logger.With("post", p.ID)
	res, err := w.doPublish(ctx, p)
	if ctx.Err() != nil {
		// Shutting down: leave the lease to expire; failExpired handles it on next start.
		return
	}
	if err != nil {
		msg := err.Error()
		log.Warn("publish failed", "err", err)
		opts := &content.TransitionOpts{ErrorMessage: &msg}
		var partial *connector.PartialError
		if errors.As(err, &partial) {
			// Keep what did go out so a retry finishes this attempt instead of publishing again.
			opts.ExternalIDs = partial.ExternalIDs
		}
		if _, terr := w.store.Transition(context.Background(), p.ID, content.StatusFailed, content.ActionFailed,
			content.ActorWorker, msg, opts); terr != nil {
			log.Error("mark failed", "err", terr)
		}
		w.broker.Publish("post.updated", map[string]any{"id": p.ID, "status": content.StatusFailed})
		return
	}
	now := time.Now().UTC()
	empty := ""
	note := fmt.Sprintf("external ids: %v", res.ExternalIDs)
	if _, err := w.store.Transition(context.Background(), p.ID, content.StatusPublished, content.ActionPublished,
		content.ActorWorker, note, &content.TransitionOpts{ExternalIDs: res.ExternalIDs, PublishedAt: &now, ErrorMessage: &empty}); err != nil {
		log.Error("mark published", "err", err)
		return
	}
	log.Info("published", "external_ids", res.ExternalIDs)
	w.broker.Publish("post.updated", map[string]any{"id": p.ID, "status": content.StatusPublished})
}

func (w *Worker) doPublish(ctx context.Context, p *content.Post) (*connector.PublishResult, error) {
	publisherID, err := w.resolvePublisher(ctx, p)
	if err != nil {
		return nil, err
	}
	pub, c, err := w.hub.Publisher(ctx, publisherID)
	if err != nil {
		return nil, err
	}
	defer hub.Close(pub)
	_ = w.store.AddAudit(ctx, p.ID, content.ActionDispatched, content.ActorWorker, "via "+c.Name)

	pctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	pctx = guard.WithPublishGrant(pctx, p.ID)
	urls, files := content.MediaFor(w.store, w.MediaBaseURL, p.MediaURLs)
	res, err := pub.Publish(pctx, connector.PublishRequest{
		PostID:      p.ID,
		Title:       p.Title,
		Platforms:   p.Platforms,
		Parts:       p.Parts(),
		Hashtags:    p.Hashtags,
		Topic:       p.Topic,
		Link:        p.LinkURL,
		MediaURLs:   urls,
		Media:       files,
		ScheduledAt: p.ScheduledAt,
		Accounts:    p.Accounts,
		Previous:    p.ExternalPostIDs,
	})
	if err != nil {
		return nil, err
	}
	if res == nil {
		res = &connector.PublishResult{}
	}
	if res.ExternalIDs == nil {
		res.ExternalIDs = map[string]string{}
	}
	return res, nil
}

func (w *Worker) resolvePublisher(ctx context.Context, p *content.Post) (uuid.UUID, error) {
	if p.PublisherConnectorID != nil {
		return *p.PublisherConnectorID, nil
	}
	c, err := w.store.FirstActiveConnector(ctx, connector.KindPublisher)
	if errors.Is(err, domain.ErrNotFound) {
		return uuid.Nil, errors.New("no active publisher connector configured (add an MCP publisher under Connectors)")
	}
	if err != nil {
		return uuid.Nil, err
	}
	return c.ID, nil
}
