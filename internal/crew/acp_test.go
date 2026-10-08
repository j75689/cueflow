package crew

import (
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/acpdrive"
	"github.com/j75689/cueflow/internal/store"
)

// CueFlow is a general agent console: an agent that does not draft posts (a coding agent, say) must
// not be told about social media or review queues; one that does keeps the rules about publishing.
func TestSystemPromptOnlyMentionsContentRulesForDraftingAgents(t *testing.T) {
	task := &store.Task{Number: 7}
	coder := &store.Agent{Name: "Dev", Role: "custom", Capabilities: []string{"planning"}}
	writer := &store.Agent{Name: "Writer", Role: "writer", Capabilities: []string{"drafting"}, Platforms: []string{"threads"}}

	got := buildSystemPrompt(coder, task, nil, nil)
	for _, s := range []string{"social media", "review queue", "draft IDs", "write drafts for"} {
		if strings.Contains(got, s) {
			t.Errorf("an agent without drafting must not see %q:\n%s", s, got)
		}
	}
	if !strings.Contains(got, "agent operations console") {
		t.Errorf("the general description is missing:\n%s", got)
	}

	got = buildSystemPrompt(writer, task, nil, nil)
	for _, s := range []string{"cannot publish to social media", "human review queue", "draft IDs", "write drafts for: threads"} {
		if !strings.Contains(got, s) {
			t.Errorf("a drafting agent must still be told %q:\n%s", s, got)
		}
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
