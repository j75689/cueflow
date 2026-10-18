// Package domain holds CueFlow's core types and the post review state machine.
// Package content is CueFlow's content workflow: posts and their review state machine, platform
// rules, the material source connectors collect, simple schedules (pipelines) that draft from it,
// the agent tools that write drafts, and publishing through a publisher connector once a human has
// approved. It is mounted on the core as a module (see Module); nothing in the core depends on it.
package content

import (
	"encoding/json"
	"errors"
	"github.com/j75689/cueflow/internal/domain"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusDraft         Status = "DRAFT"
	StatusPendingReview Status = "PENDING_REVIEW"
	StatusApproved      Status = "APPROVED"
	StatusRejected      Status = "REJECTED"
	StatusQueued        Status = "QUEUED"
	StatusPublished     Status = "PUBLISHED"
	StatusFailed        Status = "FAILED"
)

var AllStatuses = []Status{
	StatusDraft, StatusPendingReview, StatusApproved, StatusRejected,
	StatusQueued, StatusPublished, StatusFailed,
}

// transitions is the only source of truth for allowed status changes. Stages cannot be skipped:
// nothing reaches QUEUED without passing APPROVED, which only a human reviewer can set.
// FAILED -> QUEUED is the manual retry path.
var transitions = map[Status][]Status{
	StatusDraft:         {StatusPendingReview},
	StatusPendingReview: {StatusApproved, StatusRejected},
	StatusApproved:      {StatusQueued},
	StatusQueued:        {StatusPublished, StatusFailed},
	StatusFailed:        {StatusQueued},
	StatusRejected:      {},
	StatusPublished:     {},
}

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

// Editable reports whether content may still be changed by a reviewer.
func (s Status) Editable() bool { return s == StatusDraft || s == StatusPendingReview }

// Deletable reports whether a post may be removed.
func (s Status) Deletable() bool { return s == StatusDraft || s == StatusRejected }

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
	ActionGenerated  Action = "GENERATED"
	ActionCreated    Action = "CREATED"
	ActionEdited     Action = "EDITED"
	ActionSubmitted  Action = "SUBMITTED"
	ActionApproved   Action = "APPROVED"
	ActionRejected   Action = "REJECTED"
	ActionQueued     Action = "QUEUED"
	ActionDispatched Action = "DISPATCHED"
	ActionPublished  Action = "PUBLISHED"
	ActionFailed     Action = "FAILED"
	ActionRetried    Action = "RETRIED"
)

type Post struct {
	ID                   uuid.UUID         `json:"id"`
	OrgID                uuid.UUID         `json:"org_id"`
	GroupID              *uuid.UUID        `json:"group_id"`
	PipelineID           *uuid.UUID        `json:"pipeline_id"`
	RunID                *uuid.UUID        `json:"run_id"`
	TaskID               *uuid.UUID        `json:"task_id"`
	PublisherConnectorID *uuid.UUID        `json:"publisher_connector_id"`
	Title                string            `json:"title"`
	Content              string            `json:"content"`
	ThreadParts          []string          `json:"thread_parts"`
	Hashtags             []string          `json:"hashtags"`
	ImagePrompt          string            `json:"image_prompt"`
	Platforms            []string          `json:"platforms"`
	MediaURLs            []string          `json:"media_urls"`
	Accounts             []string          `json:"accounts"`
	Topic                string            `json:"topic"`    // Threads topic tag
	LinkURL              string            `json:"link_url"` // shown as a link preview card
	Status               Status            `json:"status"`
	ReviewNote           string            `json:"review_note"`
	SourceContext        json.RawMessage   `json:"source_context"`
	ErrorMessage         string            `json:"error_message"`
	ExternalPostIDs      map[string]string `json:"external_post_ids"`
	Attempts             int               `json:"attempts"`
	Version              int               `json:"version"`
	Warnings             []string          `json:"warnings"`
	ScheduledAt          *time.Time        `json:"scheduled_at"`
	PublishedAt          *time.Time        `json:"published_at"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
}

// Parts returns the ordered list of posts to publish: thread parts when present, else content.
func (p *Post) Parts() []string {
	if len(p.ThreadParts) > 0 {
		return p.ThreadParts
	}
	return []string{p.Content}
}

type AuditLog struct {
	ID          uuid.UUID       `json:"id"`
	PostID      uuid.UUID       `json:"post_id"`
	Action      Action          `json:"action"`
	Actor       Actor           `json:"actor"`
	FromStatus  Status          `json:"from_status"`
	ToStatus    Status          `json:"to_status"`
	Note        string          `json:"note"`
	PayloadDiff json.RawMessage `json:"payload_diff"`
	CreatedAt   time.Time       `json:"created_at"`
}

// ErrAlreadyRunning is returned when a pipeline is triggered while a run of it is in progress.
var ErrAlreadyRunning = errors.New("pipeline is already running")
