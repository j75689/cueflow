package runner

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/j75689/cueflow/internal/runnerapi"
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

func TestPollInterval(t *testing.T) {
	const base = `{"url":"https://x","token":"t"`
	for _, c := range []struct {
		name, body, env string
		want            time.Duration
	}{
		{"polls every 20 s by default", base + `}`, "", 20 * time.Second},
		{"set in the file", base + `,"poll_interval":5}`, "", 5 * time.Second},
		{"0 long-polls", base + `,"poll_interval":0}`, "", 0},
		{"the environment wins", base + `,"poll_interval":5}`, "0", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("CUEFLOW_RUNNER_POLL_INTERVAL", c.env)
			cfg, err := LoadConfig(writeConfig(t, c.body+"\n"))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.pollEvery(); got != c.want {
				t.Errorf("pollEvery = %s, want %s", got, c.want)
			}
		})
	}
	t.Setenv("CUEFLOW_RUNNER_POLL_INTERVAL", "soon")
	if _, err := LoadConfig(writeConfig(t, base+"}")); err == nil {
		t.Fatal("a value that is not a number of seconds must be refused")
	}
}

// claims records the claim requests a stand-in server gets; it never has work.
type claims struct {
	mu   sync.Mutex
	at   []time.Time
	wait []*int
}

func (c *claims) server(t *testing.T) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req runnerapi.ClaimRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		c.mu.Lock()
		c.at = append(c.at, time.Now())
		c.wait = append(c.wait, req.Wait)
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// A polling runner asks the server to answer at once, and waits its interval between empty claims, so no
// request is held open on the server between them.
func TestLoopPollsWithoutHoldingTheServer(t *testing.T) {
	var c claims
	every := 1
	cfg := &Config{URL: c.server(t).URL, Token: "t", Concurrency: 1, PollInterval: &every}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	if err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Loop(ctx); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.at) != 3 {
		t.Fatalf("claims in 2.5 s with a 1 s interval: %d, want 3 (at 0, 1 and 2 s)", len(c.at))
	}
	for i, w := range c.wait {
		if w == nil || *w != 0 {
			t.Fatalf("claim %d did not ask to be answered at once: %v", i, w)
		}
		if i > 0 && c.at[i].Sub(c.at[i-1]) < 900*time.Millisecond {
			t.Fatalf("claims %d and %d only %s apart", i-1, i, c.at[i].Sub(c.at[i-1]))
		}
	}
}

// With poll_interval 0 it long-polls as runners always did: no wait in the request, the server's default.
func TestLoopLongPolls(t *testing.T) {
	var c claims
	never := 0
	cfg := &Config{URL: c.server(t).URL, Token: "t", Concurrency: 1, PollInterval: &never}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Loop(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.wait) == 0 || c.wait[0] != nil {
		t.Fatalf("a long poll must leave wait to the server: %v", c.wait)
	}
}
