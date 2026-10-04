package acpagent_test

import (
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
