// Package procsweep stops the processes a runner job left behind. The runner marks every agent it starts with
// Marker(job id) in its environment, which the agent's children inherit; processes that left the agent's process
// group (setsid, nohup in a detached shell, daemons) are found by that marker. Linux only: elsewhere Find and Stop
// return ErrUnsupported and a job's processes are stopped by process group alone. A process that clears its
// environment escapes.
package procsweep

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Var is the environment variable that carries a process's job id.
const Var = "CUEFLOW_RUNNER_JOB"

// ErrUnsupported is returned on platforms without /proc.
var ErrUnsupported = errors.New("procsweep: finding processes by job is only supported on Linux")

// Marker is the environment entry that marks a process as job id's.
func Marker(id string) string { return Var + "=" + id }

// Proc is a process carrying a job's marker.
type Proc struct {
	PID  int
	Job  string
	Comm string // from /proc/<pid>/comm
}

// poll is how often Stop looks again while it waits.
const poll = 200 * time.Millisecond

// killFor is how long Stop keeps sending SIGKILL after the grace period before it gives up on processes that do
// not die (stuck in the kernel, in D state).
var killFor = 10 * time.Second

// AliveError is returned by Stop for the processes still running when it gave up.
type AliveError struct{ Procs []Proc }

func (e *AliveError) Error() string {
	alive := make([]string, len(e.Procs))
	for i, p := range e.Procs {
		alive[i] = fmt.Sprintf("%d (%s)", p.PID, p.Comm)
	}
	return fmt.Sprintf("procsweep: still running after %s of SIGKILL: %s", killFor, strings.Join(alive, ", "))
}
