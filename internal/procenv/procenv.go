// Package procenv builds the processes CueFlow starts for agents: ACP agents (acpx) and stdio MCP
// servers (mcpx), on the server and on a runner. Command is the only way to make one, so none
// inherits CueFlow's own credentials.
package procenv

import (
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/j75689/cueflow/internal/config"
)

// Command is exec.Command with the environment of Env(extra...).
func Command(name string, args []string, extra ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = Env(extra...)
	return cmd
}

// Env is this process's environment without CueFlow's own settings, followed by extra ("K=V", set by the
// connector, so it may name anything). Dropped: every config.SecretVars name and its _FILE, and every
// CUEFLOW_* and DATABASE_* variable, which also catches a credential added later. Everything else stays:
// agents rely on inherited provider credentials (ANTHROPIC_*, AWS_*, GOOGLE_*) and PATH, HOME, ….
func Env(extra ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !Withheld(k) {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}

// Withheld reports whether Env leaves the variable k out.
func Withheld(k string) bool {
	if strings.HasPrefix(k, "CUEFLOW_") || strings.HasPrefix(k, "DATABASE_") {
		return true
	}
	return slices.Contains(config.SecretVars, strings.TrimSuffix(k, "_FILE"))
}
