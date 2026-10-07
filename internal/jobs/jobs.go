// Package jobs is how CueFlow asks for background work to happen: "run this task", "publish this
// post". In the local deployment nothing needs it (in-process loops wake each other and poll the
// database as a safety net); in the cloud deployment a Queue (Cloud Tasks) delivers each request as
// an HTTP call to /internal/work/{kind}/{id}, so the process can scale to zero between requests.
//
// Delivery is at least once and handlers must be idempotent. They are: every handler starts by
// claiming its row atomically (FOR UPDATE SKIP LOCKED + lease), so a second delivery finds nothing
// to do.
package jobs

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Kind names a type of work.
type Kind string

const (
	// KindCrew claims one TODO task (id is the task id, or uuid.Nil for "whichever is next") and
	// works it to the end of its run.
	KindCrew Kind = "crew"
	// KindDispatch publishes queued posts (id is a post id, or uuid.Nil for "whichever is next").
	KindDispatch Kind = "dispatch"
	// KindPipeline runs one simple schedule (pipeline) to completion; id is the pipeline id.
	KindPipeline Kind = "pipeline"
	// KindSchedule creates the task of a recurring task schedule; id is the schedule id.
	KindSchedule Kind = "schedule"
	// KindApproval makes one approved tool call; id is the approval id.
	KindApproval Kind = "approval"
)

// Kinds lists every kind the server handles.
var Kinds = []Kind{KindCrew, KindDispatch, KindPipeline, KindSchedule, KindApproval}

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	return slices.Contains(Kinds, k)
}

// Queue delivers a unit of work to the server's own /internal/work endpoint, now or after delay.
// Enqueueing the same (kind, id) again while the first is still pending may be collapsed.
type Queue interface {
	Enqueue(ctx context.Context, kind Kind, id uuid.UUID, delay time.Duration) error
}

// Handler does the work for one delivery. A nil error acknowledges it; an error makes the queue
// retry, so return nil for "nothing to do" and for failures a retry cannot fix.
type Handler func(ctx context.Context, id uuid.UUID) error

// Handlers maps each kind to its handler.
type Handlers map[Kind]Handler

// WorkPrefix is where deliveries arrive: POST {WorkPrefix}{kind}/{id}.
const WorkPrefix = "/internal/work/"

// TickPath is what the external clock (Cloud Scheduler) calls every minute.
const TickPath = "/internal/tick"

// WorkPath is the URL path that delivers (kind, id).
func WorkPath(kind Kind, id uuid.UUID) string { return WorkPrefix + string(kind) + "/" + id.String() }

// ParseWorkPath is the inverse of WorkPath.
func ParseWorkPath(path string) (Kind, uuid.UUID, error) {
	rest, ok := strings.CutPrefix(path, WorkPrefix)
	if !ok {
		return "", uuid.Nil, fmt.Errorf("not a work path: %q", path)
	}
	kind, idText, ok := strings.Cut(rest, "/")
	if !ok {
		return "", uuid.Nil, fmt.Errorf("work path %q has no id", path)
	}
	k := Kind(kind)
	if !k.Valid() {
		return "", uuid.Nil, fmt.Errorf("unknown work kind %q", kind)
	}
	id, err := uuid.Parse(idText)
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("bad work id %q", idText)
	}
	return k, id, nil
}
