// Package acpagent is the "runtime" connector for external agents spoken to over the Agent Client
// Protocol: Claude Code, Codex, Gemini CLI, OpenCode, Goose, or any custom ACP agent. An agent
// assigned this runtime works its tasks in that external agent instead of CueFlow's built-in loop,
// using CueFlow's tools through a per-run MCP server.
package acpagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/j75689/cueflow/internal/acpx"
	"github.com/j75689/cueflow/internal/connector"
)

type preset struct {
	command string
	// binary, when found on PATH (e.g. preinstalled in the Docker image), is run instead of npx.
	binary string
	// keyEnv receives the API key field; tokenEnv the OAuth token field.
	keyEnv, tokenEnv []string
	// authMethod is passed to ACP authenticate when an API key is configured.
	authMethod string
}

// Versions pinned to what was verified; override with the Command field.
var presets = map[string]preset{
	"claude-code": {command: "npx -y @agentclientprotocol/claude-agent-acp@0.85.1", binary: "claude-agent-acp",
		keyEnv: []string{"ANTHROPIC_API_KEY"}, tokenEnv: []string{"CLAUDE_CODE_OAUTH_TOKEN"}},
	"codex": {command: "npx -y @agentclientprotocol/codex-acp@2.1.1", binary: "codex-acp",
		keyEnv: []string{"OPENAI_API_KEY", "CODEX_API_KEY"}, authMethod: "api-key"},
	"gemini":   {command: "npx -y @google/gemini-cli@0.62.0 --acp", keyEnv: []string{"GEMINI_API_KEY"}},
	"opencode": {command: "opencode acp"},
	"goose":    {command: "goose acp"},
	"custom":   {},
}

func init() {
	connector.Register(connector.Spec{
		Type:        "acp_agent",
		Kind:        connector.KindRuntime,
		Name:        "External agent (ACP)",
		Description: "Runs an agent's tasks in an external coding agent over the Agent Client Protocol (Claude Code, Codex, Gemini CLI, OpenCode, Goose...). The agent uses CueFlow's tools through MCP; drafts still go to human review. Requires the agent CLI (or Node.js for npx) on the CueFlow host.",
		Fields: []connector.Field{
			{Key: "preset", Label: "Agent", Type: connector.FieldSelect,
				Options: []string{"claude-code", "codex", "gemini", "opencode", "goose", "custom"}, Default: "claude-code"},
			{Key: "command", Label: "Command", Type: connector.FieldString,
				Help: "Override the launch command, e.g. `claude-agent-acp`. Blank uses the preset"},
			{Key: "args", Label: "Extra arguments", Type: connector.FieldStringList},
			{Key: "oauth_token", Label: "OAuth token", Type: connector.FieldPassword, Secret: true,
				Help: "Claude Code: a token from `claude setup-token` (CLAUDE_CODE_OAUTH_TOKEN) to use your Claude subscription. Blank uses the host's existing login"},
			{Key: "api_key", Label: "API key", Type: connector.FieldPassword, Secret: true,
				Help: "Claude Code: ANTHROPIC_API_KEY · Codex: OPENAI_API_KEY · Gemini: GEMINI_API_KEY"},
			{Key: "env", Label: "Environment", Type: connector.FieldKV, Help: "Extra environment variables for the agent process"},
			{Key: "auth_method", Label: "ACP auth method", Type: connector.FieldString,
				Help: "Optional ACP authenticate method id (Codex with an API key: api-key)"},
			{Key: "timeout_minutes", Label: "Run timeout (minutes)", Type: connector.FieldNumber, Default: 20},
			{Key: "allow_web", Label: "Allow web fetch/search tools", Type: connector.FieldBool, Default: true,
				Help: "Let the agent use its own web tools. Shell, file edits and other local tools are always refused"},
		},
		DefaultName: func(cfg connector.Config) string {
			if l, ok := presetLabels[cfg.String("preset")]; ok {
				return l + " (ACP)"
			}
			return "External agent (ACP)"
		},
	}, New)
}

var presetLabels = map[string]string{
	"claude-code": "Claude Code", "codex": "Codex", "gemini": "Gemini CLI",
	"opencode": "OpenCode", "goose": "Goose", "custom": "Custom agent",
}

type Runtime struct {
	launch     connector.RuntimeLaunch
	authMethod string
	allowWeb   bool
	// credHint is non-empty when the preset needs credentials and none were found.
	credHint string
}

// credentialHint explains how to authenticate a preset when neither the connector nor the server
// environment provides credentials (e.g. CueFlow in Docker cannot see the host's Claude login).
func credentialHint(name string, p preset, envs []string) string {
	wanted := append(append([]string{}, p.tokenEnv...), p.keyEnv...)
	if len(wanted) == 0 {
		return ""
	}
	for _, e := range envs {
		for _, w := range wanted {
			if strings.HasPrefix(e, w+"=") {
				return ""
			}
		}
	}
	for _, w := range wanted {
		if os.Getenv(w) != "" {
			return ""
		}
	}
	switch name {
	case "claude-code":
		return "no credentials configured: run `claude setup-token` and paste the token into OAuth token (or set an API key). The host's Claude login is not visible to CueFlow in Docker"
	case "codex":
		return "no credentials configured: set an OpenAI API key"
	case "gemini":
		return "no credentials configured: set a Gemini API key (Google AI Studio)"
	}
	return "no credentials configured"
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	name := cfg.String("preset")
	p, ok := presets[name]
	if !ok {
		return nil, fmt.Errorf("acp: unknown preset %q", name)
	}
	line := cfg.String("command")
	if line == "" {
		line = p.command
		if p.binary != "" {
			if path, err := exec.LookPath(p.binary); err == nil {
				line = strings.Replace(line, strings.Fields(line)[0], path, 1)
				if !strings.HasPrefix(p.command, p.binary) {
					line = path
				}
			}
		}
	}
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return nil, fmt.Errorf("acp: a command is required for the %q preset", name)
	}
	var envs []string
	for k, v := range cfg.KV("env") {
		envs = append(envs, k+"="+v)
	}
	if t := cfg.String("oauth_token"); t != "" {
		for _, k := range p.tokenEnv {
			envs = append(envs, k+"="+t)
		}
		if len(p.tokenEnv) == 0 {
			return nil, fmt.Errorf("acp: the %q preset does not take an OAuth token", name)
		}
	}
	authMethod := cfg.String("auth_method")
	if k := cfg.String("api_key"); k != "" {
		for _, e := range p.keyEnv {
			envs = append(envs, e+"="+k)
		}
		if authMethod == "" {
			authMethod = p.authMethod
		}
	}
	return &Runtime{
		launch: connector.RuntimeLaunch{
			Command: parts[0],
			Args:    append(parts[1:], cfg.StringList("args")...),
			Env:     envs,
			Timeout: time.Duration(cfg.Int("timeout_minutes", 20)) * time.Minute,
		},
		authMethod: authMethod,
		allowWeb:   cfg.Bool("allow_web"),
		credHint:   credentialHint(name, p, envs),
	}, nil
}

func (r *Runtime) Launch() connector.RuntimeLaunch { return r.launch }

// AuthMethod is the ACP authenticate method to call after initialize ("" = none).
func (r *Runtime) AuthMethod() string { return r.authMethod }

// CredentialHint is non-empty when no credentials were found for the preset.
func (r *Runtime) CredentialHint() string { return r.credHint }

// AllowWeb reports whether the agent may use its own fetch/search tools.
func (r *Runtime) AllowWeb() bool { return r.allowWeb }

// Test launches the agent and performs the ACP handshake. It does not create a session, so it
// costs nothing.
func (r *Runtime) Test(ctx context.Context) (*connector.TestResult, error) {
	dir, err := os.MkdirTemp("", "cueflow-acp-test-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	proc, err := acpx.Start(r.launch, dir, acpx.RejectingClient{})
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	defer proc.Close(3 * time.Second)

	// npx may download the adapter on first use.
	hctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	resp, err := acpx.Handshake(hctx, proc.Conn)
	if err != nil {
		msg := err.Error()
		if tail := proc.Stderr(); tail != "" {
			msg += ": " + lastLine(tail)
		}
		return &connector.TestResult{OK: false, Message: "handshake failed: " + msg}, nil
	}
	agent := "agent"
	if resp.AgentInfo != nil {
		agent = strings.TrimSpace(resp.AgentInfo.Name + " " + resp.AgentInfo.Version)
	}
	var auth []string
	for _, m := range resp.AuthMethods {
		auth = append(auth, authMethodID(m))
	}
	mcp := "stdio"
	if resp.AgentCapabilities.McpCapabilities.Http {
		mcp = "http"
	}
	msg := fmt.Sprintf("connected to %s (ACP v%d, MCP over %s)", agent, resp.ProtocolVersion, mcp)
	if r.credHint != "" {
		// The handshake needs no login, but every run would fail with "Authentication required".
		return &connector.TestResult{OK: false, Message: msg + ", but " + r.credHint,
			Details: map[string]any{"agent": agent, "auth_methods": auth}}, nil
	}
	return &connector.TestResult{OK: true,
		Message: msg,
		Details: map[string]any{"agent": agent, "auth_methods": auth, "mcp_http": resp.AgentCapabilities.McpCapabilities.Http}}, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func authMethodID(m acp.AuthMethod) string {
	switch {
	case m.Agent != nil:
		return m.Agent.Id
	case m.EnvVar != nil:
		return m.EnvVar.Id
	case m.Terminal != nil:
		return m.Terminal.Id
	}
	return "?"
}
