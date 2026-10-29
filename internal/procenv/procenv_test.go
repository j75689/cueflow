package procenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setCueFlowEnv sets CueFlow's own settings and a provider credential, as on a real server.
func setCueFlowEnv(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"CUEFLOW_SECRET_KEY":      "secret-key",
		"CUEFLOW_SECRET_KEY_FILE": "/run/secrets/key",
		"DATABASE_URL":            "postgres://u:p@db/cueflow",
		"CUEFLOW_ADMIN_TOKEN":     "admin-token",
		"CUEFLOW_FOO":             "foo",
		"ANTHROPIC_API_KEY":       "provider-key",
	} {
		t.Setenv(k, v)
	}
}

// assertChildEnv checks the output of `env` run as a child: CueFlow's settings are gone, the provider
// credential and the connector's own entry are there.
func assertChildEnv(t *testing.T, out string) {
	t.Helper()
	vars := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			vars[k] = v
		}
	}
	for _, k := range []string{"CUEFLOW_SECRET_KEY", "CUEFLOW_SECRET_KEY_FILE", "DATABASE_URL", "CUEFLOW_ADMIN_TOKEN", "CUEFLOW_FOO"} {
		if _, ok := vars[k]; ok {
			t.Errorf("the child sees %s", k)
		}
	}
	if vars["ANTHROPIC_API_KEY"] != "provider-key" || vars["CONNECTOR_SET"] != "yes" || vars["PATH"] == "" {
		t.Errorf("the child must keep provider credentials, PATH and the connector's entries: %v", vars)
	}
}

func TestChildDoesNotInheritCueFlowSettings(t *testing.T) {
	setCueFlowEnv(t)
	out, err := Command("env", nil, "CONNECTOR_SET=yes").Output()
	if err != nil {
		t.Fatal(err)
	}
	assertChildEnv(t, string(out))
}

func TestWithheld(t *testing.T) {
	for k, want := range map[string]bool{
		"CUEFLOW_MCP_TOKEN": true, "CUEFLOW_MCP_TOKEN_FILE": true, "DATABASE_SESSION_URL_FILE": true,
		"CUEFLOW_RUNNER_CONFIG": true, "ANTHROPIC_API_KEY": false, "AWS_PROFILE": false, "PATH": false, "HOME": false,
	} {
		if Withheld(k) != want {
			t.Errorf("Withheld(%s) = %v, want %v", k, !want, want)
		}
	}
}

// TestNoOtherWayToStartAProcess: exec.Command and exec.CommandContext are called only here, so every
// process CueFlow starts gets Env.
func TestNoOtherWayToStartAProcess(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") && path != root) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			filepath.ToSlash(filepath.Dir(path)) == "../../internal/procenv" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch x.Name + "." + sel.Sel.Name {
			case "exec.Command", "exec.CommandContext", "os.StartProcess", "syscall.Exec", "syscall.ForkExec", "syscall.StartProcess":
				t.Errorf("%s: start processes with procenv.Command, not %s.%s", fset.Position(sel.Pos()), x.Name, sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
