package acpagent_test

import (
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/connector"
	_ "github.com/j75689/cueflow/internal/connectors/acpagent"
)

func TestDefaultNamePerPreset(t *testing.T) {
	spec, _ := connector.Lookup("acp_agent")
	for preset, want := range map[string]string{"": "Claude Code (ACP)", "claude-code": "Claude Code (ACP)", "gemini": "Gemini CLI (ACP)", "codex": "Codex (ACP)"} {
		cfg := connector.Config{}
		if preset != "" {
			cfg["preset"] = preset
		}
		if got := spec.NameFor(cfg); got != want {
			t.Errorf("preset %q: got %q want %q", preset, got, want)
		}
	}
}

func TestCredentialHint(t *testing.T) {
	for _, k := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		t.Setenv(k, "")
	}
	inst, err := connector.Build("acp_agent", connector.Config{"preset": "claude-code"}, connector.Env{})
	if err != nil {
		t.Fatal(err)
	}
	h := inst.(interface{ CredentialHint() string }).CredentialHint()
	if !strings.Contains(h, "claude setup-token") {
		t.Fatalf("missing credentials should be flagged: %q", h)
	}
	inst, _ = connector.Build("acp_agent", connector.Config{"preset": "claude-code", "oauth_token": "tok"}, connector.Env{})
	if h := inst.(interface{ CredentialHint() string }).CredentialHint(); h != "" {
		t.Fatalf("token configured, hint should be empty: %q", h)
	}
	inst, _ = connector.Build("acp_agent", connector.Config{"preset": "custom", "command": "x"}, connector.Env{})
	if h := inst.(interface{ CredentialHint() string }).CredentialHint(); h != "" {
		t.Fatalf("custom agents manage their own auth: %q", h)
	}
}
