// Package external is the "runtime" connector for agents that drive their own work: CueFlow never
// starts them. They connect to the MCP endpoint with an agent token (cfa_...), claim tasks
// assigned to them, and report back through the same tools built-in agents use.
package external

import (
	"context"

	"github.com/j75689/cueflow/internal/connector"
)

func init() {
	connector.Register(connector.Spec{
		Type:        "external",
		Kind:        connector.KindRuntime,
		Name:        "External (self-driven)",
		Description: "The agent runs wherever you like (Claude Code in a terminal, Codex, a script) and pulls its own tasks over MCP with an agent token. CueFlow does not start it: tasks assigned to it wait in To do until it claims them.",
		Fields:      []connector.Field{},
		DefaultName: func(connector.Config) string { return "External (self-driven)" },
	}, func(connector.Config, connector.Env) (connector.Instance, error) { return instance{}, nil })
}

type instance struct{}

func (instance) Launch() connector.RuntimeLaunch { return connector.RuntimeLaunch{} }

func (instance) Test(context.Context) (*connector.TestResult, error) {
	return &connector.TestResult{OK: true, Message: "nothing to test: the agent connects to CueFlow's MCP endpoint with its own token"}, nil
}
