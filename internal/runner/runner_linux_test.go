package runner

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/j75689/cueflow/internal/procsweep"
	"github.com/j75689/cueflow/internal/runnerapi"
)

// stubServer takes a job's events and its completion, and has no work to hand out.
func stubServer(t *testing.T) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if strings.HasSuffix(r.URL.Path, "/claim") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// syncBuffer is a log destination the test can read while jobs write to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// stubAgent is an "agent" that leaves two sleeps behind and exits without speaking ACP. One leaves the process
// group with setsid; the other is started with nohup from a shell in a session of its own, as an agent's shell
// tool runs commands (a detached spawn).
func stubAgent(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.sh")
	script := "#!/bin/sh\n" +
		"setsid sleep 600 </dev/null >/dev/null 2>&1 &\n" +
		"setsid sh -c 'nohup sleep 600 </dev/null >/dev/null 2>&1 &'\n" +
		"sleep 0.3\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// marked starts a sleep in a session of its own, carrying job id's marker, as another job's leftover would.
func marked(t *testing.T, id string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	cmd.Env = []string{procsweep.Marker(id)}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd
}

func alive(t *testing.T, id string) []procsweep.Proc {
	t.Helper()
	procs, err := procsweep.Find([]string{id})
	if err != nil {
		t.Fatal(err)
	}
	return procs
}

// The processes a job leaves behind are stopped when it ends, and logged; another job's are not touched (job-1
// is not job-10).
func TestJobLeftoversAreStopped(t *testing.T) {
	const id, other = "runner-test-job-1", "runner-test-job-10"
	t.Cleanup(func() { _, _ = procsweep.Stop([]string{id}, 0) })
	otherProc := marked(t, other)
	var logs syncBuffer
	cfg := &Config{URL: stubServer(t).URL, Token: "t", Commands: map[string]string{"custom": stubAgent(t)}}
	r := New(cfg, slog.New(slog.NewTextHandler(&logs, nil)))
	r.runJob(context.Background(), runnerapi.Job{ID: id, Preset: "custom", TimeoutSeconds: 10})

	if procs := alive(t, id); len(procs) != 0 {
		t.Errorf("still running after the job: %+v", procs)
	}
	if !strings.Contains(logs.String(), `msg="leftover processes stopped" job=`+id+` count=2 commands=sleep,sleep`) {
		t.Errorf("no log line for the 2 sleeps:\n%s", logs.String())
	}
	if procs := alive(t, other); len(procs) != 1 || procs[0].PID != otherProc.Process.Pid {
		t.Errorf("another job's process was touched: %+v", procs)
	}
	if left := r.started(); len(left) != 0 {
		t.Errorf("jobs still listed after their sweep: %v", left)
	}
}

// Jobs still listed when the runner stops are swept on the way out.
func TestShutdownStopsListedJobs(t *testing.T) {
	ids := []string{"runner-test-left-a", "runner-test-left-b"}
	r := New(&Config{URL: stubServer(t).URL, Token: "t", Concurrency: 1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, id := range ids {
		marked(t, id)
		r.jobs[id] = true
	}
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Loop(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if procs := alive(t, id); len(procs) != 0 {
			t.Errorf("%s still running after shutdown: %+v", id, procs)
		}
	}
}
