package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "runner.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The shell is off unless the machine's owner turns it on, in the file or the environment.
func TestAllowShell(t *testing.T) {
	const base = `{"url":"https://x","token":"t"`
	for _, c := range []struct {
		name, body, env string
		want            bool
	}{
		{"off by default", base + `}`, "", false},
		{"on in the file", base + `,"allow_shell":true}`, "", true},
		{"environment turns it on", base + `}`, "true", true},
		{"environment turns it off", base + `,"allow_shell":true}`, "false", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("CUEFLOW_RUNNER_ALLOW_SHELL", c.env)
			cfg, err := LoadConfig(writeConfig(t, c.body+"\n"))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.AllowShell != c.want {
				t.Errorf("AllowShell = %v, want %v", cfg.AllowShell, c.want)
			}
		})
	}
}

func TestAllowShellRejectsGarbage(t *testing.T) {
	t.Setenv("CUEFLOW_RUNNER_ALLOW_SHELL", "sure")
	if _, err := LoadConfig(writeConfig(t, `{"url":"https://x","token":"t"}`)); err == nil {
		t.Fatal("a value that is not true or false must not silently leave the shell in either state")
	}
}
