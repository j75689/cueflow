package store

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/blob"
	"github.com/j75689/cueflow/internal/connector"
)

// The storage interfaces, split by domain. Each method is one operation of one backend: no
// transaction crosses them. A backend implements Backend (store/pg for PostgreSQL); consumers hold
// a Backend or the narrower interface they need. The methods are documented on the implementation.

// TaskStore is the board: tasks, their messages and runs, claims and leases, and schedules.
type TaskStore interface {
	GetTask(ctx context.Context, id uuid.UUID) (*Task, error)
	TaskOrg(ctx context.Context, id uuid.UUID) uuid.UUID
	GetTaskByNumber(ctx context.Context, org uuid.UUID, n int64) (*Task, error)
	ListTasks(ctx context.Context, f TaskFilter) ([]*Task, error)
	CountTasks(ctx context.Context, f TaskFilter) (map[TaskStatus]int, error)
	CreateTask(ctx context.Context, in NewTask) (*Task, error)
	UpdateTask(ctx context.Context, id uuid.UUID, p TaskPatch) (before, after *Task, err error)
	SetTaskStatusIf(ctx context.Context, id uuid.UUID, from []TaskStatus, to TaskStatus, result *string) (bool, error)
	SetClaimedTaskStatus(ctx context.Context, id, runID uuid.UUID, to TaskStatus, result *string) (bool, error)
	RequeueClaimedTask(ctx context.Context, id, runID uuid.UUID, after time.Duration) (bool, error)
	ClearTaskRetry(ctx context.Context, id uuid.UUID) (bool, error)
	RecoverResetAt(ctx context.Context, id uuid.UUID) (*time.Time, error)
	DeleteTask(ctx context.Context, id uuid.UUID) error
	TaskDepth(ctx context.Context, id uuid.UUID) (int, error)
	ClaimTodo(ctx context.Context, lease time.Duration, limit, orgDefault int) ([]Claim, error)
	ExtendTaskLease(ctx context.Context, id, runID uuid.UUID, lease time.Duration) (bool, error)
	ExtendAgentTaskLease(ctx context.Context, id uuid.UUID, lease time.Duration) error
	TaskRunID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
	HoldsClaim(ctx context.Context, id, runID uuid.UUID) (bool, error)
	AdoptTaskRun(ctx context.Context, id, runID uuid.UUID) error
	ExpiredTaskLeases(ctx context.Context) ([]ExpiredLease, error)
	OpenSubtasks(ctx context.Context, parentID uuid.UUID) (int, error)
	AddTaskMessage(ctx context.Context, taskID uuid.UUID, authorType string, agentID *uuid.UUID, kind, content string, data any) (*TaskMessage, error)
	TaskMessages(ctx context.Context, taskID uuid.UUID) ([]*TaskMessage, error)
	LastHumanMessageAt(ctx context.Context, taskID uuid.UUID) (t time.Time, ok bool, err error)
	HumanMessageSince(ctx context.Context, taskID uuid.UUID, t time.Time) (bool, error)
	StartAgentRun(ctx context.Context, runID, taskID, agentID uuid.UUID) (*AgentRun, error)
	FinishAgentRun(ctx context.Context, r *AgentRun) error
	FailAgentRun(ctx context.Context, id uuid.UUID, outcome, errText string) error
	SetRunContext(ctx context.Context, runID uuid.UUID, tokens, window int64) error
	MarkRunExternal(ctx context.Context, runID uuid.UUID, connectorID *uuid.UUID) error
	ListAgentRuns(ctx context.Context, taskID uuid.UUID) ([]*AgentRun, error)
	GetAgentRun(ctx context.Context, id uuid.UUID) (*AgentRun, error)
	HandOverAgentRun(ctx context.Context, id uuid.UUID) error
	ClaimHandedOverRuns(ctx context.Context, n int) ([]uuid.UUID, error)
	FailStaleAgentRuns(ctx context.Context) error
	ListSchedules(ctx context.Context, org *uuid.UUID) ([]*Schedule, error)
	GetSchedule(ctx context.Context, id uuid.UUID) (*Schedule, error)
	CreateSchedule(ctx context.Context, sc *Schedule, validate CronValidator) (*Schedule, error)
	UpdateSchedule(ctx context.Context, sc *Schedule, validate CronValidator) (*Schedule, error)
	DeleteSchedule(ctx context.Context, id uuid.UUID) error
	SpawnScheduledTask(ctx context.Context, scheduleID uuid.UUID) (*Task, error)
}

// AgentStore keeps agents and their memories.
type AgentStore interface {
	ListAgents(ctx context.Context, org uuid.UUID) ([]*Agent, error)
	GetAgent(ctx context.Context, id uuid.UUID) (*Agent, error)
	GetAgentBySlug(ctx context.Context, org uuid.UUID, slug string) (*Agent, error)
	CountAgents(ctx context.Context, org uuid.UUID) (int, error)
	CreateAgent(ctx context.Context, a *Agent) (*Agent, error)
	UpdateAgent(ctx context.Context, a *Agent) (*Agent, error)
	DeleteAgent(ctx context.Context, id uuid.UUID) error
	AddMemory(ctx context.Context, agentID uuid.UUID, content string) (*AgentMemory, error)
	Memories(ctx context.Context, agentID uuid.UUID, query string, limit int) ([]AgentMemory, error)
	DeleteMemory(ctx context.Context, agentID, memoryID uuid.UUID) error
}

// AgentTokenStore keeps the tokens self-driven agents act with, and their claims.
type AgentTokenStore interface {
	CreateAgentToken(ctx context.Context, agentID uuid.UUID, name string) (*AgentToken, string, error)
	ListAgentTokens(ctx context.Context, agentID uuid.UUID) ([]*AgentToken, error)
	RevokeAgentToken(ctx context.Context, agentID, id uuid.UUID) error
	AgentByToken(ctx context.Context, plain string) (*Agent, error)
	ClaimTaskForAgent(ctx context.Context, agentID uuid.UUID, taskID *uuid.UUID, lease time.Duration) (*Task, uuid.UUID, error)
	RunningAgentRun(ctx context.Context, taskID, agentID uuid.UUID) (*AgentRun, error)
	FailExternalRuns(ctx context.Context, taskID uuid.UUID, reason string) error
}

// ApprovalStore keeps tool calls that wait for a human.
type ApprovalStore interface {
	CreateToolApproval(ctx context.Context, a *ToolApproval) (*ToolApproval, error)
	GetToolApproval(ctx context.Context, id uuid.UUID) (*ToolApproval, error)
	ListToolApprovals(ctx context.Context, taskID uuid.UUID) ([]*ToolApproval, error)
	ClaimToolApproval(ctx context.Context, id uuid.UUID, status, note string) (bool, error)
	SetToolApprovalResult(ctx context.Context, id uuid.UUID, result string, isError bool, file string) error
}

// ClockStore claims due schedules and clocks, and keeps run tokens.
type ClockStore interface {
	ClaimDue(ctx context.Context, table string, now time.Time, grace time.Duration, next NextFunc) ([]uuid.UUID, error)
	ClaimClock(ctx context.Context, name string, now time.Time, grace time.Duration, next func(time.Time) (time.Time, error)) (bool, error)
	TodoTaskCount(ctx context.Context) (int, error)
	CreateRunToken(ctx context.Context, taskID, agentID uuid.UUID, runID *uuid.UUID, ttl time.Duration) (string, error)
	RunTokenByPlain(ctx context.Context, plain string) (*RunToken, error)
	SetRunTokenOutcome(ctx context.Context, plain string, outcome any) error
	DeleteRunToken(ctx context.Context, plain string) error
	PurgeRunTokens(ctx context.Context) error
	FailAbandoned(ctx context.Context) error
}

// ConnectorStore keeps connectors and their sealed secrets.
type ConnectorStore interface {
	ListConnectors(ctx context.Context, kind string) ([]*Connector, error)
	GetConnector(ctx context.Context, id uuid.UUID) (*Connector, error)
	CreateConnector(ctx context.Context, name, typ string, active bool, settings map[string]any) (*Connector, error)
	UpdateConnector(ctx context.Context, id uuid.UUID, patch ConnectorPatch) (*Connector, error)
	DeleteConnector(ctx context.Context, id uuid.UUID) error
	ConnectorSettings(ctx context.Context, id uuid.UUID) (*Connector, connector.Config, error)
	SetConnectorCheck(ctx context.Context, id uuid.UUID, ok bool, msg string) error
	FirstActiveConnector(ctx context.Context, kind connector.Kind) (*Connector, error)
	ConnectorState(id uuid.UUID) connector.StateStore
	ReencryptConnectorSecrets(ctx context.Context, dryRun bool) (*RotationReport, error)
	SetToolPermissions(ctx context.Context, id uuid.UUID, perms map[string]string) error
	CountConnectors(ctx context.Context) (total, active int, err error)
}

// MediaStore keeps media and their bytes.
type MediaStore interface {
	CreateMedia(ctx context.Context, in NewMedia) (*Media, error)
	GetMedia(ctx context.Context, id uuid.UUID) (*Media, error)
	MediaRedirect(ctx context.Context, id uuid.UUID, ttl time.Duration) (url string, ok bool, err error)
	OpenMedia(ctx context.Context, id uuid.UUID) (*Media, io.ReadSeekCloser, error)
	MigrateMedia(ctx context.Context, dst blob.Store) (int, error)
	AdoptMedia(ctx context.Context, staged, createdBy string, validate func(head []byte, size int64) (string, error)) (*Media, error)
	DeleteMedia(ctx context.Context, id uuid.UUID) error
	PruneMedia(ctx context.Context, olderThan time.Duration, dryRun bool) (int, error)
}

// OrgStore keeps orgs.
type OrgStore interface {
	ListOrgs(ctx context.Context) ([]*Org, error)
	GetOrg(ctx context.Context, id uuid.UUID) (*Org, error)
	DefaultOrg(ctx context.Context) (*Org, error)
	ResolveOrg(ctx context.Context, ref string) (*Org, error)
	CreateOrg(ctx context.Context, o *Org) (*Org, error)
	UpdateOrg(ctx context.Context, o *Org) (*Org, error)
	DeleteOrg(ctx context.Context, id uuid.UUID) error
}

// RunnerStore keeps runners, their registrations and their jobs.
type RunnerStore interface {
	CreateRegistration(ctx context.Context, labels []string, orgIDs []uuid.UUID, ttl time.Duration) (string, time.Time, error)
	RegisterRunner(ctx context.Context, regToken, name, version, platform string, wantLabels []string, concurrency int) (*Runner, string, error)
	RunnerByToken(ctx context.Context, plain string) (*Runner, error)
	GetRunner(ctx context.Context, id uuid.UUID) (*Runner, error)
	ListRunners(ctx context.Context) ([]*Runner, error)
	UpdateRunner(ctx context.Context, id uuid.UUID, p RunnerPatch) (*Runner, error)
	ReportRunner(ctx context.Context, r *Runner, version string, concurrency int) (*Runner, bool, error)
	DeleteRunner(ctx context.Context, id uuid.UUID) error
	CreateRunnerJob(ctx context.Context, in NewRunnerJob) (*RunnerJob, error)
	GetRunnerJob(ctx context.Context, id uuid.UUID) (*RunnerJob, error)
	RunnerJobForRun(ctx context.Context, runID uuid.UUID) (*RunnerJob, error)
	ListTaskRunnerJobs(ctx context.Context, taskID uuid.UUID) ([]*RunnerJob, error)
	ClaimRunnerJob(ctx context.Context, r *Runner) (*RunnerJob, error)
	HeartbeatRunnerJob(ctx context.Context, id, runnerID uuid.UUID) (string, error)
	CompleteRunnerJob(ctx context.Context, id, runnerID uuid.UUID, status string, result any) (bool, error)
	EndRunnerJob(ctx context.Context, id uuid.UUID, status string, result any) (bool, error)
	CancelOrphanRunnerJobs(ctx context.Context) error
	StopRunnerJob(ctx context.Context, id uuid.UUID) error
}

// TaskFileStore keeps tasks' working files.
type TaskFileStore interface {
	WriteTaskFile(ctx context.Context, taskID uuid.UUID, name, mimeType string, data []byte, createdBy string) (*TaskFile, error)
	CreateTaskFile(ctx context.Context, taskID uuid.UUID, name, mimeType string, data []byte, createdBy string) (*TaskFile, error)
	CountTaskFiles(ctx context.Context, taskID uuid.UUID, prefix string) (int, error)
	ListTaskFiles(ctx context.Context, taskID uuid.UUID, limit int) (files []*ListedTaskFile, total int, err error)
	AncestorTaskFile(ctx context.Context, taskID uuid.UUID, name string) (number int64, ok bool, err error)
	OpenTaskFile(ctx context.Context, taskID, owner uuid.UUID, name string) (*TaskFile, io.ReadSeekCloser, error)
	DeleteTaskFile(ctx context.Context, taskID uuid.UUID, name string) error
	SubtasksWithFile(ctx context.Context, taskID uuid.UUID, name string) ([]int64, error)
	ReadTaskFileRange(ctx context.Context, taskID, owner uuid.UUID, name string, offset, limit int64) (*TaskFile, []byte, error)
}

// UsageStore reports token usage.
type UsageStore interface {
	UsageReport(ctx context.Context, since time.Time, tz string) (*Usage, error)
}

// Backend is every domain store of one database, plus its lifecycle.
type Backend interface {
	TaskStore
	AgentStore
	AgentTokenStore
	ApprovalStore
	ClockStore
	ConnectorStore
	MediaStore
	OrgStore
	RunnerStore
	TaskFileStore
	UsageStore
	Ping(ctx context.Context) error
	// SetBlobs makes new media and task-file bytes go to b instead of the database.
	SetBlobs(b blob.Store)
	Close()
}
