// Package runnerapi is the wire protocol between CueFlow and its Runners (HTTP + JSON under
// /api/runner/v1). The server stores a Job as a runner job's payload and a CompleteRequest as its
// result.
package runnerapi

// ProtocolVersion is bumped on incompatible changes; the server answers 426 to older runners.
const ProtocolVersion = 1

// BasePath is where the runner API is mounted.
const BasePath = "/api/runner/v1"

// ClaimWait is the longest a claim request waits for work before answering 204.
const ClaimWait = 25 // seconds

// DefaultPollInterval is how long a runner waits between claims that found no work. It asks with Wait 0, so
// the server answers at once: a request held open is what Cloud Run bills, and a runner that long-polled kept
// the service billed around the clock. A job is picked up at most this much later.
const DefaultPollInterval = 20 // seconds

type RegisterRequest struct {
	Token       string   `json:"token"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Platform    string   `json:"platform"`
	Labels      []string `json:"labels"`
	Concurrency int      `json:"concurrency"`
	Protocol    int      `json:"protocol"`
}

type RegisterResponse struct {
	RunnerID string   `json:"runner_id"`
	Token    string   `json:"token"`
	Name     string   `json:"name"`
	Labels   []string `json:"labels"`
}

type ClaimRequest struct {
	Protocol int `json:"protocol"`
	// Wait is how many seconds the server may hold the request for work to arrive, up to ClaimWait; 0 answers
	// at once. Absent means ClaimWait: the long poll of runners from before this field.
	Wait *int `json:"wait,omitempty"`
}

// Job is one unit of work for a runner.
type Job struct {
	ID     string `json:"id"`
	TaskID string `json:"task_id"`
	Title  string `json:"title"`
	// Preset names the agent CLI to launch ("claude-code", "codex", ...); the runner resolves it
	// to a command on its own machine and may refuse it.
	Preset string `json:"preset"`
	// Model is the model to ask the agent for (id or name); blank keeps the agent's default.
	Model  string `json:"model,omitempty"`
	Prompt string `json:"prompt"`
	// MCPPath is CueFlow's per-run tool server; the runner prefixes the URL it registered with.
	MCPPath        string `json:"mcp_path"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	AllowWeb       bool   `json:"allow_web"`
}

// Event is something the agent did, to be shown in the task thread.
type Event struct {
	// Kind is "message", "tool_call", "tool_result" or "system".
	Kind    string `json:"kind"`
	Content string `json:"content"`
	Data    any    `json:"data,omitempty"`
}

type EventsRequest struct {
	Events []Event `json:"events"`
}

// JobStatus answers events and heartbeats; Cancel tells the runner to stop the agent.
type JobStatus struct {
	Cancel bool `json:"cancel"`
}

type CompleteRequest struct {
	// StopReason is the ACP stop reason ("end_turn", "cancelled", ...), empty on failure.
	StopReason   string `json:"stop_reason"`
	FinalText    string `json:"final_text"`
	AgentName    string `json:"agent_name"`
	ToolCalls    int    `json:"tool_calls"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	// Error is set when the agent could not run or failed.
	Error    string `json:"error"`
	TimedOut bool   `json:"timed_out"`
	Canceled bool   `json:"canceled"`
}
