// Package guard enforces FR-4.2: the agent may never trigger a publish, or any other action a
// human has to approve. Acting requires a grant that only a worker attaches, after it has claimed
// an item (a post, a deliverable) that a human approved. Agent runs are additionally marked so an
// attempt from inside an agent context fails even if a grant leaked into it.
package guard

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

var ErrPublishForbidden = errors.New("publishing is only allowed for human-approved posts via the dispatch worker")

type grantKey struct{}
type agentKey struct{}

// WithPublishGrant authorizes publishing of a single approved post. Only internal/dispatch calls it.
func WithPublishGrant(ctx context.Context, postID uuid.UUID) context.Context {
	return context.WithValue(ctx, grantKey{}, postID)
}

// PublishGrant returns the post the context is authorized to publish.
func PublishGrant(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(grantKey{}).(uuid.UUID)
	return id, ok && id != uuid.Nil
}

// WithAgent marks a context as running agent (LLM-driven) code.
func WithAgent(ctx context.Context) context.Context {
	return context.WithValue(ctx, agentKey{}, true)
}

func InAgent(ctx context.Context) bool {
	v, _ := ctx.Value(agentKey{}).(bool)
	return v
}

// CheckPublish returns nil only for a dispatch-granted, non-agent context publishing postID.
func CheckPublish(ctx context.Context, postID uuid.UUID) error {
	if InAgent(ctx) {
		return ErrPublishForbidden
	}
	granted, ok := PublishGrant(ctx)
	if !ok || granted != postID {
		return ErrPublishForbidden
	}
	return nil
}
