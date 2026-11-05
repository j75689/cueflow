package deliverable

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/store"
)

// Store is the deliverables' persistence: the core backend plus the module's own queries. The
// implementation lives with the backend (deliverable/pg, deliverable/sqlite) and embeds the core store.
type Store interface {
	store.Backend
	Get(ctx context.Context, id uuid.UUID) (*Deliverable, error)
	List(ctx context.Context, f Filter) ([]*Deliverable, int, error)
	Create(ctx context.Context, in New, actor Actor, submit bool) (*Deliverable, error)
	Update(ctx context.Context, id uuid.UUID, version int, patch Patch, actor Actor, note string) (*Deliverable, error)
	Transition(ctx context.Context, id uuid.UUID, to Status, action Action, actor Actor, note string, opts *TransitionOpts) (*Deliverable, error)
	Remove(ctx context.Context, id uuid.UUID) error
	AddAudit(ctx context.Context, id uuid.UUID, action Action, actor Actor, note string) error
	AuditLogs(ctx context.Context, id uuid.UUID) ([]AuditLog, error)
	ForTask(ctx context.Context, taskID uuid.UUID) ([]Ref, error)
	CountByStatus(ctx context.Context, org uuid.UUID) (map[Status]int, error)
	QueueApproved(ctx context.Context) ([]*Deliverable, error)
	ClaimQueued(ctx context.Context, lease time.Duration, limit int) ([]*Deliverable, error)
	ExpiredLeases(ctx context.Context) ([]uuid.UUID, error)
	ActionableCount(ctx context.Context) (int, error)
	// MediaInUse reports whether an unfinished deliverable uses media id (store.MediaUse).
	MediaInUse(ctx context.Context, id uuid.UUID) (bool, error)
}

// NewStore returns the deliverable store over the core backend s, for code that holds only the core
// (the agents' tools). The backends' packages set it (deliverable/pg, deliverable/sqlite).
var NewStore func(s store.Backend) Store

// Filter selects deliverables for List.
type Filter struct {
	OrgID    *uuid.UUID
	TaskID   *uuid.UUID
	Kind     string
	Statuses []Status
	Query    string
	Limit    int
	Offset   int
}

// New is the input for Create.
type New struct {
	// OrgID may be left empty: it then comes from the task, else the default org.
	OrgID             uuid.UUID
	TaskID            *uuid.UUID
	Kind              string
	Title             string
	Payload           json.RawMessage
	ActionConnectorID *uuid.UUID
}

// Patch carries edits; nil fields are left unchanged.
type Patch struct {
	Title             *string
	Payload           json.RawMessage
	ActionConnectorID **uuid.UUID
}

// TransitionOpts sets extra columns as part of a status change.
type TransitionOpts struct {
	ReviewNote        *string
	ActionConnectorID **uuid.UUID
	ErrorMessage      *string
	ExternalIDs       map[string]string
	Result            map[string]any
	ActedAt           *time.Time
	// Check, when set, vets the current row after the state machine has allowed the move and before it is
	// made; its error aborts the transition.
	Check func(ctx context.Context, cur *Deliverable) error
}

// Ref is what a task shows of its deliverables.
type Ref struct {
	ID     uuid.UUID `json:"id"`
	Kind   string    `json:"kind"`
	Title  string    `json:"title"`
	Status Status    `json:"status"`
}
