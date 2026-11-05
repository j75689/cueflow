package procsweep

import (
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"
)

// start runs a shell script in a session of its own (out of reach of any process group) with job id's marker.
func start(t *testing.T, id, script string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin", Marker(id)}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }() // reap it: an unreaped child would stay a zombie
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd
}

func pids(t *testing.T, ids ...string) []int {
	t.Helper()
	procs, err := Find(ids)
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, p := range procs {
		out = append(out, p.PID)
	}
	return out
}

// The marker must match a whole environment entry: job-1 is not job-10.
func TestFindMatchesTheWholeEntry(t *testing.T) {
	one := start(t, "procsweep-job-1", "exec sleep 600")
	ten := start(t, "procsweep-job-10", "exec sleep 600")
	time.Sleep(100 * time.Millisecond)
	for _, c := range []struct {
		id   string
		want int
	}{
		{"procsweep-job-1", one.Process.Pid},
		{"procsweep-job-10", ten.Process.Pid},
	} {
		if got := pids(t, c.id); !slices.Equal(got, []int{c.want}) {
			t.Errorf("Find(%s) = %v, want [%d]", c.id, got, c.want)
		}
	}
	if got := pids(t, "procsweep-job"); len(got) != 0 {
		t.Errorf("Find(procsweep-job) = %v, want none (a prefix is not a match)", got)
	}
}

// A process that ignores SIGTERM is killed once the grace period is over; others' processes are left alone.
func TestStopKillsAfterGrace(t *testing.T) {
	stubborn := start(t, "procsweep-stubborn", "trap '' TERM; while :; do sleep 1; done")
	other := start(t, "procsweep-other", "exec sleep 600")
	time.Sleep(100 * time.Millisecond)
	began := time.Now()
	stopped, err := Stop([]string{"procsweep-stubborn"}, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took < 500*time.Millisecond {
		t.Errorf("Stop returned after %s, before the grace period was over", took)
	}
	if got := pids(t, "procsweep-stubborn"); len(got) != 0 {
		t.Errorf("still running after Stop: %v", got)
	}
	if len(stopped) == 0 || stopped[0].PID != stubborn.Process.Pid || stopped[0].Comm != "sh" {
		t.Errorf("Stop reported %+v, want the stubborn sh (pid %d) first", stopped, stubborn.Process.Pid)
	}
	if got := pids(t, "procsweep-other"); !slices.Equal(got, []int{other.Process.Pid}) {
		t.Errorf("another job's process was touched: Find = %v, want [%d]", got, other.Process.Pid)
	}
}
