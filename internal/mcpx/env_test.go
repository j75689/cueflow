package mcpx

import (
	"strings"
	"testing"
)

// TestStdioServerDoesNotInheritCueFlowSettings runs `env` as a stdio MCP server would be run.
func TestStdioServerDoesNotInheritCueFlowSettings(t *testing.T) {
	t.Setenv("CUEFLOW_SECRET_KEY", "secret-key")
	t.Setenv("CUEFLOW_SECRET_KEY_FILE", "/run/secrets/key")
	t.Setenv("DATABASE_URL", "postgres://u:p@db/cueflow")
	t.Setenv("CUEFLOW_ADMIN_TOKEN", "admin-token")
	t.Setenv("CUEFLOW_FOO", "foo")
	t.Setenv("ANTHROPIC_API_KEY", "provider-key")
	out, err := stdioCommand(Options{Transport: TransportStdio, Command: "env", Env: map[string]string{"CONNECTOR_SET": "yes"}}).Output()
	if err != nil {
		t.Fatal(err)
	}
	env := "\n" + string(out)
	for _, k := range []string{"CUEFLOW_SECRET_KEY=", "CUEFLOW_SECRET_KEY_FILE=", "DATABASE_URL=", "CUEFLOW_ADMIN_TOKEN=", "CUEFLOW_FOO="} {
		if strings.Contains(env, "\n"+k) {
			t.Errorf("the server sees %s", k)
		}
	}
	for _, kv := range []string{"ANTHROPIC_API_KEY=provider-key", "CONNECTOR_SET=yes"} {
		if !strings.Contains(env, "\n"+kv+"\n") {
			t.Errorf("the server must see %s", kv)
		}
	}
}
