// Package deliverable is the general form of "an agent produces something, a human approves it,
// then the system acts": a typed item with a payload, a review state machine with an audit log,
// and an action connector that is run once, under a grant, after approval. The content module's
// posts are the special case this was derived from; they keep their own table and worker.
//
// It is mounted on the core as a module: REST under /deliverables, a task's deliverables in
// GET /tasks/{id}, list_deliverables on the operator MCP endpoint, and the "deliverables"
// capability (create, list and revise) for agents.
package deliverable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
)

type Status string

const (
	StatusDraft         Status = "DRAFT"
	StatusPendingReview Status = "PENDING_REVIEW"
	StatusApproved      Status = "APPROVED"
	StatusRejected      Status = "REJECTED"
	StatusQueued        Status = "QUEUED"
	StatusDone          Status = "DONE"
	StatusFailed        Status = "FAILED"
)

var AllStatuses = []Status{StatusDraft, StatusPendingReview, StatusApproved, StatusRejected, StatusQueued, StatusDone, StatusFailed}

// transitions is the state machine; only a human moves an item to APPROVED or REJECTED.
var transitions = map[Status][]Status{
	StatusDraft:         {StatusPendingReview},
	StatusPendingReview: {StatusApproved, StatusRejected},
	StatusApproved:      {StatusQueued},
	StatusQueued:        {StatusDone, StatusFailed},
	StatusFailed:        {StatusQueued},
	StatusRejected:      {},
	StatusDone:          {},
}

// Transitions returns a copy of the state machine for /meta.
func Transitions() map[Status][]Status {
	out := make(map[Status][]Status, len(transitions))
	for k, v := range transitions {
		out[k] = append([]Status{}, v...)
	}
	return out
}

func (s Status) Valid() bool {
	_, ok := transitions[s]
	return ok
}

func (s Status) CanTransitionTo(to Status) bool {
	for _, t := range transitions[s] {
		if t == to {
			return true
		}
	}
	return false
}

// Editable reports whether the payload may still be changed.
func (s Status) Editable() bool { return s == StatusDraft || s == StatusPendingReview }

// Removable reports whether the item may be removed.
func (s Status) Removable() bool { return s == StatusDraft || s == StatusRejected }

// CheckTransition validates from -> to.
func CheckTransition(from, to Status) error {
	if !from.CanTransitionTo(to) {
		return &domain.TransitionError{From: string(from), To: string(to)}
	}
	return nil
}

type Actor string

const (
	ActorAgent    Actor = "system_agent"
	ActorWorker   Actor = "system_worker"
	ActorReviewer Actor = "human_reviewer"
)

type Action string

const (
	ActionCreated   Action = "CREATED"
	ActionEdited    Action = "EDITED"
	ActionSubmitted Action = "SUBMITTED"
	ActionApproved  Action = "APPROVED"
	ActionRejected  Action = "REJECTED"
	ActionQueued    Action = "QUEUED"
	ActionStarted   Action = "STARTED"
	ActionDone      Action = "DONE"
	ActionFailed    Action = "FAILED"
	ActionRetried   Action = "RETRIED"
)

// Deliverable is one reviewable item.
type Deliverable struct {
	ID                uuid.UUID         `json:"id"`
	OrgID             uuid.UUID         `json:"org_id"`
	TaskID            *uuid.UUID        `json:"task_id"`
	Kind              string            `json:"kind"`
	Title             string            `json:"title"`
	Payload           json.RawMessage   `json:"payload"`
	Status            Status            `json:"status"`
	ReviewNote        string            `json:"review_note"`
	ErrorMessage      string            `json:"error_message"`
	ActionConnectorID *uuid.UUID        `json:"action_connector_id"`
	ExternalIDs       map[string]string `json:"external_ids"`
	Result            json.RawMessage   `json:"result"`
	Attempts          int               `json:"attempts"`
	LeaseUntil        *time.Time        `json:"lease_until,omitempty"`
	Version           int               `json:"version"`
	CreatedBy         Actor             `json:"created_by"`
	ActedAt           *time.Time        `json:"acted_at"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// AuditLog is one entry of an item's history.
type AuditLog struct {
	ID            uuid.UUID       `json:"id"`
	DeliverableID uuid.UUID       `json:"deliverable_id"`
	Action        Action          `json:"action"`
	Actor         Actor           `json:"actor"`
	FromStatus    Status          `json:"from_status"`
	ToStatus      Status          `json:"to_status"`
	Note          string          `json:"note"`
	PayloadDiff   json.RawMessage `json:"payload_diff,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

// Kind is a type of deliverable: what its payload looks like and how it is checked. Modules
// register theirs with RegisterKind; "document" is built in.
type Kind struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Schema is the payload's JSON schema, shown to agents and the dashboard.
	Schema map[string]any `json:"schema,omitempty"`
	// Validate rejects a payload that does not fit the kind; nil accepts any JSON object.
	Validate func(payload json.RawMessage) error `json:"-"`
	// Approve, when set, says whether a reviewer may approve the item as it stands, beyond the payload's
	// shape (the content module refuses a post its publisher could not take). It runs inside the approval
	// transaction, holding the item's row lock, and its own queries take other pool connections: keep it to
	// quick store lookups, never a network call (such as asking a publisher for its accounts). Return a
	// *store.ValidationError for a reason the reviewer can fix.
	Approve func(ctx context.Context, d *Deliverable) error `json:"-"`
	// Act, when set, is what approval leads to for this kind, instead of an action connector: the
	// worker calls it once per attempt under the item's grant (the content module publishes a
	// "post" this way). Previous attempts' external ids are on the item.
	Act func(ctx context.Context, d *Deliverable) (*connector.ActResult, error) `json:"-"`
	// BuiltIn reports in /meta that the kind acts by itself, so no action connector is needed.
	BuiltIn bool `json:"built_in,omitempty"`
}

var kinds struct {
	mu   sync.RWMutex
	byID map[string]Kind
}

// RegisterKind adds or replaces a kind.
func RegisterKind(k Kind) {
	kinds.mu.Lock()
	defer kinds.mu.Unlock()
	if kinds.byID == nil {
		kinds.byID = map[string]Kind{}
	}
	kinds.byID[k.ID] = k
}

// Kinds lists the registered kinds, sorted by id.
func Kinds() []Kind {
	kinds.mu.RLock()
	defer kinds.mu.RUnlock()
	out := make([]Kind, 0, len(kinds.byID))
	for _, k := range kinds.byID {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// KindByID looks a kind up.
func KindByID(id string) (Kind, bool) {
	kinds.mu.RLock()
	defer kinds.mu.RUnlock()
	k, ok := kinds.byID[id]
	return k, ok
}

func kindIDs() []string {
	var ids []string
	for _, k := range Kinds() {
		ids = append(ids, k.ID)
	}
	return ids
}

// validatePayload checks that payload is a JSON object the kind accepts.
func validatePayload(kind string, payload json.RawMessage) (json.RawMessage, error) {
	k, ok := KindByID(strings.TrimSpace(kind))
	if !ok {
		return nil, fmt.Errorf("unknown deliverable kind %q (registered: %s)", kind, strings.Join(kindIDs(), ", "))
	}
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil || obj == nil {
		return nil, errors.New("payload must be a JSON object")
	}
	if k.Validate != nil {
		if err := k.Validate(payload); err != nil {
			return nil, fmt.Errorf("%s: %w", k.ID, err)
		}
	}
	return payload, nil
}

// DocumentKind is the built-in kind: a Markdown document.
const DocumentKind = "document"

func init() {
	RegisterKind(Kind{ID: DocumentKind, Name: "Document",
		Description: "A Markdown document (a report, a plan, a spec, release notes...). The action connector decides where an approved document goes.",
		Schema: map[string]any{"type": "object", "required": []string{"content"}, "properties": map[string]any{
			"content": map[string]any{"type": "string", "description": "The document in Markdown"},
			"summary": map[string]any{"type": "string", "description": "One or two sentences for the reviewer (optional)"},
		}},
		Validate: func(payload json.RawMessage) error {
			var d struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(payload, &d); err != nil {
				return err
			}
			if strings.TrimSpace(d.Content) == "" {
				return errors.New("content is required")
			}
			return nil
		}})
}
