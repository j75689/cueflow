package crew

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// A stalled run's entry, replaced by a newer run of the same task, is not removed when the stalled
// run ends: Cancel still reaches the newer run.
func TestStaleRunLeavesTheNewerRunTracked(t *testing.T) {
	c := &Crew{running: map[uuid.UUID]*runSlot{}}
	id := uuid.New()
	_, cancelOld := context.WithCancelCause(context.Background())
	newCtx, cancelNew := context.WithCancelCause(context.Background())
	untrackOld := c.track(id, cancelOld)
	untrackNew := c.track(id, cancelNew)

	untrackOld()
	if !c.Cancel(id) || context.Cause(newCtx) != errStopped {
		t.Fatal("the stale run's exit removed the newer run's entry")
	}
	untrackNew()
	if c.Cancel(id) {
		t.Fatal("the newer run's exit should remove its entry")
	}
}
