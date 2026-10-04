// Package acpx launches external agents that speak the Agent Client Protocol (ACP) over stdio and
// manages their process lifecycle. The protocol itself is handled by github.com/coder/acp-go-sdk.
package acpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/j75689/cueflow/internal/connector"
)

// Process is a running ACP agent.
type Process struct {
	Conn   *acp.ClientSideConnection
	cmd    *exec.Cmd
	stderr *tailBuffer
	once   sync.Once
	exited chan struct{}
}

// Start launches the agent described by launch with client handling agent->client requests.
// The process gets its own process group so Close can stop everything it spawned.
func Start(launch connector.RuntimeLaunch, cwd string, client acp.Client) (*Process, error) {
	if strings.TrimSpace(launch.Command) == "" {
		return nil, errors.New("acp: no command configured")
	}
	cmd := exec.Command(launch.Command, launch.Args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), launch.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	tail := &tailBuffer{max: 8 << 10}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("acp: %q not found on this host: external agents need Node.js/the agent CLI "+
				"(in Docker use the cueflow:agents image)", launch.Command)
		}
		return nil, fmt.Errorf("acp: start %s: %w", launch.Command, err)
	}
	p := &Process{cmd: cmd, stderr: tail, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.exited)
	}()
	p.Conn = acp.NewClientSideConnection(client, stdin, stdout)
	return p, nil
}

// Stderr returns the last few KB the agent wrote to stderr (useful in error messages).
func (p *Process) Stderr() string { return strings.TrimSpace(p.stderr.String()) }

// Exited is closed when the process has exited.
func (p *Process) Exited() <-chan struct{} { return p.exited }

// Close terminates the agent's process group: SIGTERM, then SIGKILL after grace.
func (p *Process) Close(grace time.Duration) {
	p.once.Do(func() {
		pgid := -p.cmd.Process.Pid
		_ = syscall.Kill(pgid, syscall.SIGTERM)
		select {
		case <-p.exited:
		case <-time.After(grace):
			_ = syscall.Kill(pgid, syscall.SIGKILL)
			<-p.exited
		}
	})
}

// Handshake runs initialize and returns the agent's response.
func Handshake(ctx context.Context, conn *acp.ClientSideConnection) (acp.InitializeResponse, error) {
	title := "CueFlow"
	return conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		// No client file system or terminal: agents must not read or write the CueFlow host
		// through us. Task actions go through CueFlow's MCP tools instead.
		ClientCapabilities: acp.ClientCapabilities{Fs: acp.FileSystemCapabilities{}, Terminal: false},
		ClientInfo:         &acp.Implementation{Name: "cueflow", Title: &title, Version: "0.1.0"},
	})
}

// RejectingClient answers agent requests conservatively: every permission request is rejected,
// file system and terminal requests fail. Embed it and override what you need.
type RejectingClient struct{}

var errUnsupported = errors.New("not supported by CueFlow")

func (RejectingClient) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, errUnsupported
}
func (RejectingClient) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errUnsupported
}
func (RejectingClient) RequestPermission(_ context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	return acp.RequestPermissionResponse{Outcome: RejectOutcome(p.Options)}, nil
}
func (RejectingClient) SessionUpdate(context.Context, acp.SessionNotification) error { return nil }
func (RejectingClient) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, errUnsupported
}
func (RejectingClient) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errUnsupported
}
func (RejectingClient) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errUnsupported
}
func (RejectingClient) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errUnsupported
}
func (RejectingClient) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errUnsupported
}

// AllowOutcome picks a one-time allow option (falling back to any allow option).
func AllowOutcome(opts []acp.PermissionOption) (acp.RequestPermissionOutcome, bool) {
	for _, kinds := range [][]acp.PermissionOptionKind{{acp.PermissionOptionKindAllowOnce}, {acp.PermissionOptionKindAllowAlways}} {
		for _, o := range opts {
			for _, k := range kinds {
				if o.Kind == k {
					return acp.NewRequestPermissionOutcomeSelected(o.OptionId), true
				}
			}
		}
	}
	return acp.RequestPermissionOutcome{}, false
}

// RejectOutcome picks a one-time reject option, or cancels if the agent offered none.
func RejectOutcome(opts []acp.PermissionOption) acp.RequestPermissionOutcome {
	for _, k := range []acp.PermissionOptionKind{acp.PermissionOptionKindRejectOnce, acp.PermissionOptionKindRejectAlways} {
		for _, o := range opts {
			if o.Kind == k {
				return acp.NewRequestPermissionOutcomeSelected(o.OptionId)
			}
		}
	}
	return acp.NewRequestPermissionOutcomeCancelled()
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

var _ io.Writer = (*tailBuffer)(nil)
