package crew

import (
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/acpdrive"
)

// A runner that allows the shell swaps acpdrive.ShellRule in the prompt; if the prompt stopped
// containing that exact text the swap would silently do nothing and the agent would still be told
// the shell is unavailable.
func TestPromptHoldsTheShellRule(t *testing.T) {
	if !strings.Contains(acpPrompt, acpdrive.ShellRule) {
		t.Fatal("acpPrompt must contain acpdrive.ShellRule verbatim")
	}
}
