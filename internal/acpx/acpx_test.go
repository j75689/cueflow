package acpx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/j75689/cueflow/internal/connector"
)

// Close returns soon after the agent exits even when a process it left outside its group still holds its stderr.
func TestCloseDoesNotWaitForStderrHolder(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid on this host")
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	// The grandchild writes its pid once it is in a session of its own, still holding the agent's stderr.
	script := "setsid sh -c 'echo $$ >" + pidFile + "; exec sleep 600' & sleep 600"
	p, err := Start(connector.RuntimeLaunch{Command: "sh", Args: []string{"-c", script}}, t.TempDir(), RejectingClient{})
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	for deadline := time.Now().Add(5 * time.Second); pid == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			p.Close(0)
			t.Fatal("the grandchild did not start")
		}
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	done := make(chan struct{})
	go func() {
		p.Close(time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close still waiting after 10s")
	}
}
