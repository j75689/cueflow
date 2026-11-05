package procsweep

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Find lists this user's processes (other than this one) whose environment has the marker of one of ids, as an
// exact entry. A process that is gone, cannot be read or has an empty environment (a zombie) is skipped.
func Find(ids []string) ([]Proc, error) {
	want := make(map[string]string, len(ids))
	for _, id := range ids {
		want[Marker(id)] = id
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("procsweep: %w", err)
	}
	uid, self := uint32(os.Getuid()), os.Getpid()
	var out []Proc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		dir := "/proc/" + e.Name()
		fi, err := os.Stat(dir)
		if err != nil || fi.Sys().(*syscall.Stat_t).Uid != uid {
			continue
		}
		env, err := os.ReadFile(dir + "/environ")
		if err != nil {
			continue
		}
		for _, kv := range bytes.Split(env, []byte{0}) {
			if id, ok := want[string(kv)]; ok {
				comm, _ := os.ReadFile(dir + "/comm")
				out = append(out, Proc{PID: pid, Job: id, Comm: strings.TrimSpace(string(comm))})
				break
			}
		}
	}
	return out, nil
}

// Stop ends every process Find(ids) reports: SIGTERM, then a look every 200 ms for up to grace (a process that
// turned up meanwhile gets its SIGTERM too), then SIGKILL until none is left. The list is read again before every
// signal, so a pid is never signalled from a stale list. It returns each process it signalled, once.
func Stop(ids []string, grace time.Duration) ([]Proc, error) {
	seen := map[int]bool{}
	var stopped []Proc
	signal := func(procs []Proc, sig syscall.Signal) {
		for _, p := range procs {
			if !seen[p.PID] {
				seen[p.PID] = true
				stopped = append(stopped, p)
			} else if sig == syscall.SIGTERM {
				continue
			}
			_ = syscall.Kill(p.PID, sig)
		}
	}
	deadline := time.Now().Add(grace)
	for {
		procs, err := Find(ids)
		if err != nil {
			return stopped, err
		}
		if len(procs) == 0 {
			return stopped, nil
		}
		if time.Now().Before(deadline) || len(seen) == 0 {
			signal(procs, syscall.SIGTERM)
		} else {
			signal(procs, syscall.SIGKILL)
		}
		time.Sleep(poll)
	}
}
