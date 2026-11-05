// Package store holds the domain types every storage backend shares, the interfaces they implement
// (interfaces.go) and the pure helpers around them. The SQL lives in the backend packages (store/pg).
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
)

// AgentTokenPrefix marks credentials that act as an agent over MCP.
const AgentTokenPrefix = "cfa_"

// ExternalRuntimeType is the runtime connector type for agents that drive their own work: the
// server never starts them; they claim tasks over MCP.
const ExternalRuntimeType = "external"

type AgentToken struct {
	ID         uuid.UUID  `json:"id"`
	AgentID    uuid.UUID  `json:"agent_id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

var AgentRoles = []string{"manager", "researcher", "writer", "editor", "custom"}

type AgentStats struct {
	OpenTasks int `json:"open_tasks"`
	Running   int `json:"running"`
}

type Agent struct {
	ID                uuid.UUID  `json:"id"`
	OrgID             uuid.UUID  `json:"org_id"`
	Name              string     `json:"name"`
	Slug              string     `json:"slug"`
	Avatar            string     `json:"avatar"`
	Role              string     `json:"role"`
	Description       string     `json:"description"`
	Instructions      string     `json:"instructions"`
	Capabilities      []string   `json:"capabilities"`
	LLMConnectorID    *uuid.UUID `json:"llm_connector_id"`
	SearchConnectorID *uuid.UUID `json:"search_connector_id"`
	// RuntimeConnectorID selects an external ACP agent runtime; nil = built-in LLM loop.
	RuntimeConnectorID *uuid.UUID  `json:"runtime_connector_id"`
	SourceConnectorIDs []uuid.UUID `json:"source_connector_ids"`
	// ToolConnectorIDs are the external tool servers (MCP) this agent may use.
	ToolConnectorIDs []uuid.UUID `json:"tool_connector_ids"`
	Language         string      `json:"language"`
	Platforms        []string    `json:"platforms"`
	MaxTurns         int         `json:"max_turns"`
	// AutoHandOff continues a run that handed off (with a note) in a fresh run without waiting for a
	// human, a few times in a row at most (built-in runtime only).
	AutoHandOff bool `json:"auto_handoff"`
	// AutoRecover is how many runs in a row that failed for a transient reason (a connection or
	// provider error, a lost runner, an interrupted server) start again on their own after a backoff,
	// instead of waiting for a human; 0 = off, at most 10. Not for external (self-driven) runtimes,
	// whose agents report their own runs.
	AutoRecover int        `json:"auto_recover"`
	IsActive    bool       `json:"is_active"`
	Stats       AgentStats `json:"stats"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (a *Agent) Has(capability string) bool {
	for _, c := range a.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidCapability is injected by package crew (which owns the tool catalog).
var ValidCapability = func(string) bool { return true }

// ValidatePlatforms checks an agent's platform list; the content module injects the platform
// catalog, without it any value is accepted.
var ValidatePlatforms = func([]string) error { return nil }

// Normalize trims and defaults an agent's fields and validates them; a backend calls it before writing.
func (a *Agent) Normalize() error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return Invalid("name is required")
	}
	a.Slug = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(a.Slug, "@")))
	if a.Slug == "" {
		a.Slug = Slugify(a.Name)
	}
	if !slugRe.MatchString(a.Slug) {
		return Invalid("slug may only contain lowercase letters, digits and dashes")
	}
	if strings.TrimSpace(a.Avatar) == "" {
		a.Avatar = "🤖"
	}
	if a.Role == "" {
		a.Role = "custom"
	}
	validRole := false
	for _, r := range AgentRoles {
		if r == a.Role {
			validRole = true
		}
	}
	if !validRole {
		return Invalid(fmt.Sprintf("unknown role %q", a.Role))
	}
	seen := map[string]bool{}
	caps := []string{}
	for _, c := range a.Capabilities {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		if !ValidCapability(c) {
			return Invalid(fmt.Sprintf("unknown capability %q", c))
		}
		seen[c] = true
		caps = append(caps, c)
	}
	a.Capabilities = caps
	if len(a.Platforms) > 0 {
		if err := ValidatePlatforms(a.Platforms); err != nil {
			return Invalid(err.Error())
		}
	} else {
		a.Platforms = []string{}
	}
	if a.Language == "" {
		a.Language = "en"
	}
	if a.MaxTurns <= 0 {
		a.MaxTurns = 12
	}
	if a.RuntimeConnectorID != nil {
		a.AutoHandOff = false // only the built-in runtime hands off
	}
	if a.MaxTurns > 40 {
		a.MaxTurns = 40
	}
	a.AutoRecover = min(max(a.AutoRecover, 0), 10)
	if a.SourceConnectorIDs == nil {
		a.SourceConnectorIDs = []uuid.UUID{}
	}
	return nil
}

type AgentMemory struct {
	ID        uuid.UUID `json:"id"`
	AgentID   uuid.UUID `json:"agent_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// ToolApproval is an agent's call to an "ask" tool, waiting for or decided by a human.
type ToolApproval struct {
	ID          uuid.UUID  `json:"id"`
	TaskID      uuid.UUID  `json:"task_id"`
	AgentID     *uuid.UUID `json:"agent_id"`
	ConnectorID *uuid.UUID `json:"connector_id"`
	ServerName  string     `json:"server_name"`
	Tool        string     `json:"tool"`
	// CalledAs is the name the agent called the tool by (sanitized and deduplicated by the crew).
	CalledAs string `json:"called_as"`
	// ResultFile is the task file a long result was saved to, or "".
	ResultFile string          `json:"result_file"`
	Arguments  json.RawMessage `json:"arguments"`
	Status     string          `json:"status"` // pending | approved | rejected
	Result     string          `json:"result"`
	IsError    bool            `json:"is_error"`
	Note       string          `json:"note"`
	CreatedAt  time.Time       `json:"created_at"`
	DecidedAt  *time.Time      `json:"decided_at"`
}

// NextFunc computes the next activation of a cron expression after t.
type NextFunc func(expr, tz string, t time.Time) (time.Time, error)

// ClockTables are the tables ClaimDue may read: rows with cron_expr, timezone, is_active and
// next_run_at columns. Modules append theirs.
var ClockTables = []string{"schedules"}

// RunToken is the database record behind a per-run MCP URL.
type RunToken struct {
	TaskID, AgentID uuid.UUID
	// RunID is the agent run the token serves (see HoldsClaim); nil for tokens from before it was kept.
	RunID *uuid.UUID
	// Outcome is what a terminal tool call decided ({"kind","text"}); nil while the run is open.
	Outcome json.RawMessage
}

// Connector is a configured plugin instance as stored. Secret values never leave the store
// through this type; SecretsSet lists which secret keys currently hold a value.
type Connector struct {
	ID            uuid.UUID      `json:"id"`
	Name          string         `json:"name"`
	Kind          connector.Kind `json:"kind"`
	Type          string         `json:"type"`
	IsActive      bool           `json:"is_active"`
	Config        map[string]any `json:"config"`
	SecretsSet    []string       `json:"secrets_set"`
	LastStatus    *string        `json:"last_status"`
	LastError     string         `json:"last_error"`
	LastCheckedAt *time.Time     `json:"last_checked_at"`
	// ToolPermissions maps tool name -> allow|ask|deny for tool servers (kind "tools").
	ToolPermissions map[string]string `json:"tool_permissions"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`

	// SealedSecrets is the secrets column as stored (sealed with the secret box); only a backend
	// reads it, and it is never serialized.
	SealedSecrets []byte `json:"-"`
}

// ConnectorPatch: Settings keys present with a value replace it; a null value clears it. Secret keys
// sent as "" keep the stored secret (the UI never receives secret values to echo back).
type ConnectorPatch struct {
	Name     *string
	IsActive *bool
	Settings map[string]any
}

// RotationReport describes a re-encryption pass over connector secrets.
type RotationReport struct {
	Total          int      `json:"total"`           // connectors with stored secrets
	AlreadyPrimary int      `json:"already_primary"` // already sealed with the primary key
	Reencrypted    int      `json:"reencrypted"`     // sealed with a previous key, now re-sealed
	Undecryptable  []string `json:"undecryptable"`   // names of connectors no configured key can open
	DryRun         bool     `json:"dry_run"`
}

// Media is an image CueFlow stores itself (uploaded or generated).
type Media struct {
	ID          uuid.UUID  `json:"id"`
	MimeType    string     `json:"mime_type"`
	SizeBytes   int        `json:"size_bytes"`
	Source      string     `json:"source"` // upload | generated
	Prompt      string     `json:"prompt"`
	ConnectorID *uuid.UUID `json:"connector_id"`
	CreatedBy   string     `json:"created_by"`
	Data        []byte     `json:"-"`
}

type NewMedia struct {
	MimeType    string
	Data        []byte
	Source      string
	Prompt      string
	ConnectorID *uuid.UUID
	CreatedBy   string
}

// ErrMediaInUse means a deliverable that is still going to be reviewed or acted on uses the media.
var ErrMediaInUse = errors.New("media is used by a deliverable that is not finished")

// Org is a team with its own agents, board, schedules, deliverables and runs.
// Connectors and media are global.
type Org struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Slug string    `json:"slug"`
	// NotifierID is the notifier connector the org's task alerts go to; nil sends none.
	NotifierID *uuid.UUID `json:"notifier_id"`
	// AgentConcurrency is how many of the org's tasks the built-in crew works at once, counted across
	// every instance; nil uses CUEFLOW_AGENT_CONCURRENCY.
	AgentConcurrency *int      `json:"agent_concurrency"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// MaxOrgAgentConcurrency is the highest agent_concurrency an org may set.
const MaxOrgAgentConcurrency = 64

// Normalize trims and defaults an org's fields and validates them; a backend calls it before writing.
func (o *Org) Normalize() error {
	o.Name = strings.TrimSpace(o.Name)
	if o.Name == "" {
		return Invalid("name is required")
	}
	o.Slug = strings.ToLower(strings.TrimSpace(o.Slug))
	if o.Slug == "" {
		o.Slug = Slugify(o.Name)
		if strings.HasPrefix(o.Slug, "agent-") && !strings.HasPrefix(strings.ToLower(o.Name), "agent") {
			o.Slug = "org-" + strings.TrimPrefix(o.Slug, "agent-")
		}
	}
	if !slugRe.MatchString(o.Slug) {
		return Invalid("slug may only contain lowercase letters, digits and dashes")
	}
	return nil
}

// Token prefixes: a registration token is traded once for a runner token.
const (
	RegistrationTokenPrefix = "cfreg_"
	RunnerTokenPrefix       = "cfr_"
)

// RunnerOnlineWindow is how recently a runner must have polled to count as online.
const RunnerOnlineWindow = 90 * time.Second

// RunnerLease is how long a claimed job stays reserved without a heartbeat.
const RunnerLease = 2 * time.Minute

// Job states.
const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
	// JobCanceling: the server wants the runner to stop; the runner still reports its last events
	// and completes the job.
	JobCanceling = "canceling"
	JobCanceled  = "canceled"
	JobExpired   = "expired"
)

type Runner struct {
	ID             uuid.UUID   `json:"id"`
	Name           string      `json:"name"`
	Labels         []string    `json:"labels"`
	OrgIDs         []uuid.UUID `json:"org_ids"`
	IsActive       bool        `json:"is_active"`
	MaxConcurrency int         `json:"max_concurrency"`
	Version        string      `json:"version"`
	Platform       string      `json:"platform"`
	LastSeenAt     *time.Time  `json:"last_seen_at"`
	CreatedAt      time.Time   `json:"created_at"`
	// Online and RunningJobs are filled by ListRunners.
	Online      bool `json:"online"`
	RunningJobs int  `json:"running_jobs"`
}

type RunnerPatch struct {
	Name     *string
	Labels   *[]string
	OrgIDs   *[]uuid.UUID
	IsActive *bool
}

// MaxRunnerConcurrency is the most jobs a runner is given at once, whatever it reports.
const MaxRunnerConcurrency = 16

type RunnerJob struct {
	ID         uuid.UUID       `json:"id"`
	OrgID      uuid.UUID       `json:"org_id"`
	TaskID     uuid.UUID       `json:"task_id"`
	AgentRunID *uuid.UUID      `json:"agent_run_id"`
	Labels     []string        `json:"labels"`
	LabelMatch string          `json:"label_match"`
	Status     string          `json:"status"`
	RunnerID   *uuid.UUID      `json:"runner_id"`
	RunnerName string          `json:"runner_name"`
	LeaseUntil *time.Time      `json:"lease_until"`
	Payload    json.RawMessage `json:"-"`
	Result     json.RawMessage `json:"result"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
}

type NewRunnerJob struct {
	OrgID      uuid.UUID
	TaskID     uuid.UUID
	AgentRunID *uuid.UUID
	Labels     []string
	// LabelMatch is LabelMatchAll (blank) or LabelMatchAny.
	LabelMatch string
	Payload    any
}

// How a runner job's labels match a runner's (runner_jobs.label_match).
const (
	LabelMatchAll = "all" // the runner has every label
	LabelMatchAny = "any" // the runner has at least one
)

// CronValidator is injected by the scheduler so the store does not depend on the cron library.
type CronValidator func(expr, tz string) error

// ValidationError is a caller error (bad input) as opposed to a storage failure.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// TaskFile is a file in a task's working storage. Its bytes live where media bytes do (the shared
// byte layer in blobbed.go), but in their own table: the public /media/{id} route never sees them.
type TaskFile struct {
	ID        uuid.UUID `json:"id"`
	TaskID    uuid.UUID `json:"task_id"`
	Name      string    `json:"name"`
	MimeType  string    `json:"mime_type"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const (
	// MaxTaskFileBytes caps one task file.
	MaxTaskFileBytes = 8 << 20
	maxTaskFileName  = 200
)

// ErrTaskFileExists is returned by CreateTaskFile when the task already has a file of that name.
var ErrTaskFileExists = errors.New("the task already has a file of that name")

// TaskFileHeadBytes is how much of each file ListTaskFiles returns in Head.
const TaskFileHeadBytes = 400

// ListedTaskFile is a file as ListTaskFiles returns it: with the number of the task it belongs to
// and the start of its bytes, so a listing never has to open the files.
type ListedTaskFile struct {
	TaskFile
	TaskNumber int64 `json:"task_number"`
	// Subtask is true for a file of a direct subtask, which is read only by naming that task.
	Subtask bool   `json:"subtask"`
	Head    []byte `json:"-"`
}

// ErrTaskFileNotReadable is returned for a file of a task that is not taskID, one of its ancestors
// or one of its direct subtasks.
var ErrTaskFileNotReadable = errors.New("that task's files can't be read from this task")

type TaskStatus string

const (
	TaskBacklog      TaskStatus = "BACKLOG"
	TaskTodo         TaskStatus = "TODO"
	TaskInProgress   TaskStatus = "IN_PROGRESS"
	TaskBlocked      TaskStatus = "BLOCKED"
	TaskWaitingHuman TaskStatus = "WAITING_HUMAN"
	TaskReview       TaskStatus = "REVIEW"
	TaskDone         TaskStatus = "DONE"
	TaskCanceled     TaskStatus = "CANCELED"
)

var TaskStatuses = []TaskStatus{TaskBacklog, TaskTodo, TaskInProgress, TaskBlocked, TaskWaitingHuman, TaskReview, TaskDone, TaskCanceled}

var TaskPriorities = []string{"low", "normal", "high", "urgent"}

func (s TaskStatus) Valid() bool {
	for _, v := range TaskStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// Closed reports whether the task is finished from the board's point of view.
func (s TaskStatus) Closed() bool { return s == TaskDone || s == TaskCanceled || s == TaskReview }

type AgentRef struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Slug   string    `json:"slug"`
	Avatar string    `json:"avatar"`
}

type SubtaskCounts struct {
	Total int `json:"total"`
	Done  int `json:"done"`
}

type Task struct {
	ID              uuid.UUID  `json:"id"`
	OrgID           uuid.UUID  `json:"org_id"`
	Number          int64      `json:"number"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Status          TaskStatus `json:"status"`
	Priority        string     `json:"priority"`
	Labels          []string   `json:"labels"`
	AssigneeAgentID *uuid.UUID `json:"assignee_agent_id"`
	Assignee        *AgentRef  `json:"assignee"`
	CreatedBy       string     `json:"created_by"`
	CreatorAgentID  *uuid.UUID `json:"creator_agent_id"`
	ParentID        *uuid.UUID `json:"parent_id"`
	ScheduleID      *uuid.UUID `json:"schedule_id"`
	// PublisherConnectorID and PublishAccounts are applied to drafts agents write for this task.
	PublisherConnectorID *uuid.UUID    `json:"publisher_connector_id"`
	PublishAccounts      []string      `json:"publish_accounts"`
	SubtaskCounts        SubtaskCounts `json:"subtask_counts"`
	Result               string        `json:"result"`
	DueAt                *time.Time    `json:"due_at"`
	Position             float64       `json:"position"`
	Running              bool          `json:"running"`
	Attempts             int           `json:"attempts"`
	Version              int           `json:"version"`
	CreatedAt            time.Time     `json:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at"`
	StartedAt            *time.Time    `json:"started_at"`
	CompletedAt          *time.Time    `json:"completed_at"`
}

type TaskFilter struct {
	OrgID      *uuid.UUID
	Statuses   []TaskStatus
	AssigneeID *uuid.UUID
	Unassigned bool
	ParentID   *uuid.UUID
	Query      string
	All        bool // include old DONE/CANCELED
	CreatedBy  string
	Limit      int
	Offset     int
	Sort       TaskSort
}

// TaskSort is the order ListTasks returns tasks in.
type TaskSort string

const (
	TaskSortPosition TaskSort = ""       // position, created_at, id
	TaskSortRecent   TaskSort = "recent" // most recently finished (or updated) first, then id
)

type NewTask struct {
	// OrgID may be left empty: it then comes from the parent task, the schedule or the assignee,
	// and falls back to the default org.
	OrgID           uuid.UUID
	Title           string
	Description     string
	Status          TaskStatus
	Priority        string
	Labels          []string
	AssigneeAgentID *uuid.UUID
	CreatedBy       string // human | agent
	CreatorAgentID  *uuid.UUID
	ParentID        *uuid.UUID
	ScheduleID      *uuid.UUID
	DueAt           *time.Time
	// Publishing defaults for drafts made for this task.
	PublisherConnectorID *uuid.UUID
	PublishAccounts      []string
}

type TaskPatch struct {
	Title           *string
	Description     *string
	Status          *TaskStatus
	Priority        *string
	Labels          *[]string
	AssigneeAgentID **uuid.UUID
	DueAt           **time.Time
	Position        *float64
	// RunNow is the human's Run: with Status, it resets an auto-recover count even on a TODO task.
	RunNow bool
	// Publishing defaults: both are set together.
	PublisherConnectorID **uuid.UUID
	PublishAccounts      *[]string
}

// Claim is one claim of a task. Every claim mints the id of the run that works it and records it on
// the task (tasks.run_id): the run extends the lease and finishes the task only while run_id is still
// its own, so a run that stalled past its lease while the task was claimed again finds it lost.
type Claim struct {
	ID    uuid.UUID
	RunID uuid.UUID
}

// ExpiredLease is an IN_PROGRESS task whose runner died, with the run of that claim (nil if the
// claim recorded none), read together so a later claim's run is never taken for it.
type ExpiredLease struct {
	ID    uuid.UUID
	RunID *uuid.UUID
}

type TaskMessage struct {
	ID         uuid.UUID       `json:"id"`
	TaskID     uuid.UUID       `json:"task_id"`
	Seq        int64           `json:"seq"`
	AuthorType string          `json:"author_type"`
	AgentID    *uuid.UUID      `json:"agent_id"`
	Agent      *AgentRef       `json:"agent"`
	Kind       string          `json:"kind"`
	Content    string          `json:"content"`
	Data       json.RawMessage `json:"data"`
	CreatedAt  time.Time       `json:"created_at"`
}

type AgentRun struct {
	ID           uuid.UUID  `json:"id"`
	TaskID       uuid.UUID  `json:"task_id"`
	AgentID      *uuid.UUID `json:"agent_id"`
	Status       string     `json:"status"`
	Turns        int        `json:"turns"`
	ToolCalls    int        `json:"tool_calls"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	// CacheReadTokens and CacheWriteTokens are the parts of InputTokens served from and written to
	// the provider's prompt cache.
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	// ContextTokens is the prompt of the run's latest model call, how full the context is (InputTokens
	// adds up every call). ContextWindow is the model's limit, 0 when unknown.
	ContextTokens int64 `json:"context_tokens"`
	ContextWindow int64 `json:"context_window"`
	// Outcome is how the run ended: complete, ask, approval, wait, handoff, overflow, limit, error,
	// canceled, interrupted, continued, retry (failed, and auto-recover sent the task back to TODO)
	// ("" while running, and for runs before it was recorded;
	// "handover" while a running run waits for the next delivery to keep watching it).
	Outcome string `json:"outcome"`
	Error   string `json:"error"`
	// ConnectorID is the LLM connector (built-in runtime) or the ACP runtime connector used.
	ConnectorID *uuid.UUID `json:"connector_id"`
	Model       string     `json:"model"`
	Runtime     string     `json:"runtime"` // builtin | acp
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

type Schedule struct {
	ID              uuid.UUID  `json:"id"`
	OrgID           uuid.UUID  `json:"org_id"`
	Name            string     `json:"name"`
	CronExpr        string     `json:"cron_expr"`
	Timezone        string     `json:"timezone"`
	IsActive        bool       `json:"is_active"`
	AssigneeAgentID *uuid.UUID `json:"assignee_agent_id"`
	TaskTitle       string     `json:"task_title"`
	TaskDescription string     `json:"task_description"`
	Priority        string     `json:"priority"`
	CreatedBy       string     `json:"created_by"`
	CreatorAgentID  *uuid.UUID `json:"creator_agent_id"`
	// Publishing defaults copied to the tasks this schedule creates.
	PublisherConnectorID *uuid.UUID `json:"publisher_connector_id"`
	PublishAccounts      []string   `json:"publish_accounts"`
	LastRunAt            *time.Time `json:"last_run_at"`
	NextRunAt            *time.Time `json:"next_run_at"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// Normalize trims and defaults a schedule's fields and validates them (the cron expression with
// validate, when set); a backend calls it before writing.
func (sc *Schedule) Normalize(validate CronValidator) error {
	sc.Name = strings.TrimSpace(sc.Name)
	sc.TaskTitle = strings.TrimSpace(sc.TaskTitle)
	if sc.TaskTitle == "" {
		return Invalid("task title is required")
	}
	if sc.Name == "" {
		sc.Name = sc.TaskTitle
	}
	if sc.Timezone == "" {
		sc.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(sc.Timezone); err != nil {
		return Invalid(fmt.Sprintf("unknown timezone %q", sc.Timezone))
	}
	if sc.Priority == "" {
		sc.Priority = "normal"
	}
	if !ValidPriority(sc.Priority) {
		return Invalid("priority must be low, normal, high or urgent")
	}
	if sc.CreatedBy == "" {
		sc.CreatedBy = "human"
	}
	sc.CronExpr = strings.TrimSpace(sc.CronExpr)
	if validate != nil {
		if err := validate(sc.CronExpr, sc.Timezone); err != nil {
			return Invalid(fmt.Sprintf("invalid schedule: %v", err))
		}
	}
	return nil
}

type UsageTotals struct {
	Runs           int64 `json:"runs"`
	FailedRuns     int64 `json:"failed_runs"`
	InputTokens    int64 `json:"input_tokens"`
	OutputTokens   int64 `json:"output_tokens"`
	ToolCalls      int64 `json:"tool_calls"`
	UnreportedRuns int64 `json:"unreported_runs"`
	// CacheReadTokens and CacheWriteTokens are the parts of InputTokens read from and written to the
	// providers' prompt caches.
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

type UsageDay struct {
	Date             string `json:"date"`
	Runs             int64  `json:"runs"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

type UsageSource struct {
	Source           string `json:"source"`
	Runs             int64  `json:"runs"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

type UsageAgent struct {
	AgentID          uuid.UUID `json:"agent_id"`
	Name             string    `json:"name"`
	Avatar           string    `json:"avatar"`
	Slug             string    `json:"slug"`
	Runs             int64     `json:"runs"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
}

type UsageModel struct {
	ConnectorID      *uuid.UUID `json:"connector_id"`
	ConnectorName    string     `json:"connector_name"`
	ConnectorType    string     `json:"connector_type"`
	Runtime          string     `json:"runtime"`
	Model            string     `json:"model"`
	Runs             int64      `json:"runs"`
	InputTokens      int64      `json:"input_tokens"`
	OutputTokens     int64      `json:"output_tokens"`
	CacheReadTokens  int64      `json:"cache_read_tokens"`
	CacheWriteTokens int64      `json:"cache_write_tokens"`
}

type UsageOrg struct {
	OrgID            uuid.UUID `json:"org_id"`
	Name             string    `json:"name"`
	Slug             string    `json:"slug"`
	Runs             int64     `json:"runs"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
}

type UsageTask struct {
	TaskID           uuid.UUID `json:"task_id"`
	Number           int64     `json:"number"`
	Title            string    `json:"title"`
	Runs             int64     `json:"runs"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
}

type Usage struct {
	From     time.Time     `json:"from"`
	To       time.Time     `json:"to"`
	TZ       string        `json:"tz"`
	Totals   UsageTotals   `json:"totals"`
	Daily    []UsageDay    `json:"daily"`
	BySource []UsageSource `json:"by_source"`
	ByOrg    []UsageOrg    `json:"by_org"`
	ByAgent  []UsageAgent  `json:"by_agent"`
	ByModel  []UsageModel  `json:"by_model"`
	TopTasks []UsageTask   `json:"top_tasks"`
}
