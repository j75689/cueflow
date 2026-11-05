// Package procsweep stops the processes a runner job left behind. The runner marks every agent it starts with
// Marker(job id) in its environment, which the agent's children inherit; processes that left the agent's process
// group (setsid, nohup in a detached shell, daemons) are found by that marker. Linux only: elsewhere Find and Stop
// return ErrUnsupported and a job's processes are stopped by process group alone. A process that clears its
// environment escapes.
package procsweep

import (
	"errors"
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
