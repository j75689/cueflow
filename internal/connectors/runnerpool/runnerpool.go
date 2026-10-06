// Package runnerpool is the "runtime" connector for agents that run on registered Runners: machines
// that poll CueFlow for work and run the agent CLI locally with their own credentials. Nothing
// secret lives here; the connector only says which runners may take the work.
package runnerpool

import (
	"context"
	"time"

	"github.com/j75689/cueflow/internal/connector"
)

// Type is the connector type id.
const Type = "runner_pool"

func init() {
	connector.Register(connector.Spec{
		Type:        Type,
		Kind:        connector.KindRuntime,
		Name:        "Runner pool",
		Description: "Runs an agent's tasks on a registered Runner (your own machine), using the agent CLI and credentials on that machine. The Runner polls CueFlow for work, so it needs no inbound connection. Add runners under Runners.",
		Fields: []connector.Field{
			{Key: "labels", Label: "Runner labels", Type: connector.FieldStringList,
				Help: "A runner must have every label listed here to take the work, e.g. claude-code. Blank accepts any runner"},
			{Key: "preset", Label: "Agent", Type: connector.FieldSelect,
				Options: []string{"claude-code", "codex", "gemini", "opencode", "goose", "custom"}, Default: "claude-code",
				Help: "Which agent CLI the runner launches. \"custom\" runs whatever the runner maps to it in its own config. The runner's policy can still refuse it"},
			{Key: "model", Label: "Model", Type: connector.FieldString,
				Help: "Model id or name as the agent lists it (e.g. gemini-pro-agent, claude-sonnet-5-5). Blank uses the agent's default. A runner can pin its own"},
			{Key: "timeout_minutes", Label: "Run timeout (minutes)", Type: connector.FieldNumber, Default: 20},
			{Key: "queue_timeout_minutes", Label: "Wait for a runner (minutes)", Type: connector.FieldNumber, Default: 30,
				Help: "Fail the task if no runner picks the work up in this time"},
			{Key: "allow_web", Label: "Allow web fetch/search tools", Type: connector.FieldBool, Default: true,
				Help: "Let the agent use its own web tools. The runner's local policy can only narrow this"},
		},
		DefaultName: func(cfg connector.Config) string { return "Runner pool" },
	}, New)
}

// Pool is a configured runner pool.
type Pool struct {
	labels       []string
	preset       string
	model        string
	timeout      time.Duration
	queueTimeout time.Duration
	allowWeb     bool
}

func New(cfg connector.Config, _ connector.Env) (connector.Instance, error) {
	p := &Pool{
		labels:       cfg.StringList("labels"),
		preset:       cfg.String("preset"),
		model:        cfg.String("model"),
		timeout:      time.Duration(cfg.Int("timeout_minutes", 20)) * time.Minute,
		queueTimeout: time.Duration(cfg.Int("queue_timeout_minutes", 30)) * time.Minute,
		allowWeb:     true,
	}
	if p.preset == "" {
		p.preset = "claude-code"
	}
	if _, set := cfg["allow_web"]; set {
		p.allowWeb = cfg.Bool("allow_web")
	}
	return p, nil
}

func (p *Pool) Labels() []string                { return p.labels }
func (p *Pool) Model() string                   { return p.model }
func (p *Pool) Preset() string                  { return p.preset }
func (p *Pool) RunTimeout() time.Duration       { return p.timeout }
func (p *Pool) QueueTimeout() time.Duration     { return p.queueTimeout }
func (p *Pool) AllowWeb() bool                  { return p.allowWeb }
func (p *Pool) AuthMethod() string              { return "" }
func (p *Pool) Launch() connector.RuntimeLaunch { return connector.RuntimeLaunch{Timeout: p.timeout} }

// Test only reports configuration: whether a runner is online is a question for the Runners page.
func (p *Pool) Test(context.Context) (*connector.TestResult, error) {
	return &connector.TestResult{OK: true, Message: "runner pool configured; check the Runners page for online runners"}, nil
}
