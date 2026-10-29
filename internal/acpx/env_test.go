package acpx

import (
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/connector"
)

// TestAgentDoesNotInheritCueFlowSettings runs `env` as the agent would be run.
func TestAgentDoesNotInheritCueFlowSettings(t *testing.T) {
	t.Setenv("CUEFLOW_SECRET_KEY", "secret-key")
	t.Setenv("CUEFLOW_SECRET_KEY_FILE", "/run/secrets/key")
	t.Setenv("DATABASE_URL", "postgres://u:p@db/cueflow")
	t.Setenv("CUEFLOW_ADMIN_TOKEN", "admin-token")
	t.Setenv("CUEFLOW_FOO", "foo")
	t.Setenv("ANTHROPIC_API_KEY", "provider-key")
	out, err := command(connector.RuntimeLaunch{Command: "env", Env: []string{"CONNECTOR_SET=yes"}}, t.TempDir()).Output()
	if err != nil {
		t.Fatal(err)
	}
	env := "\n" + string(out)
	for _, k := range []string{"CUEFLOW_SECRET_KEY=", "CUEFLOW_SECRET_KEY_FILE=", "DATABASE_URL=", "CUEFLOW_ADMIN_TOKEN=", "CUEFLOW_FOO="} {
		if strings.Contains(env, "\n"+k) {
			t.Errorf("the agent sees %s", k)
		}
	}
	for _, kv := range []string{"ANTHROPIC_API_KEY=provider-key", "CONNECTOR_SET=yes"} {
		if !strings.Contains(env, "\n"+kv+"\n") {
			t.Errorf("the agent must see %s", kv)
		}
	}
}
