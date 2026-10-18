package crew

import (
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/acpdrive"
	"github.com/j75689/cueflow/internal/store"
)

// CueFlow is a general agent console: the core prompt names no domain. What a capability adds
// (the content module's publishing rules, say) is that module's business and tested there.
func TestSystemPromptIsDomainNeutral(t *testing.T) {
	task := &store.Task{Number: 7}
	coder := &store.Agent{Name: "Dev", Role: "custom", Capabilities: []string{"planning"}}
	got := buildSystemPrompt(coder, task, nil, nil)
	for _, s := range []string{"social media", "review queue", "draft", "post"} {
		if strings.Contains(strings.ToLower(got), s) {
			t.Errorf("the core prompt must not mention %q:\n%s", s, got)
		}
	}
	if !strings.Contains(got, "agent operations console") {
		t.Errorf("the general description is missing:\n%s", got)
	}
}

// A runner that allows the shell swaps acpdrive.ShellRule in the prompt; if the prompt stopped
// containing that exact text the swap would silently do nothing and the agent would still be told
// the shell is unavailable.
func TestPromptHoldsTheShellRule(t *testing.T) {
	if !strings.Contains(acpPrompt, acpdrive.ShellRule) {
		t.Fatal("acpPrompt must contain acpdrive.ShellRule verbatim")
	}
}
