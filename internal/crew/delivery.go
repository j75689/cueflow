package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/runnerapi"
	"github.com/j75689/cueflow/internal/store"
)

// In the cloud a run works inside one queued delivery: an HTTP request from Cloud Tasks that is cut
// off after DeliveryLimit. A cut-off run used to read as "Stopped." and wait for a human, and a
// runner's agent was killed with it. Instead, each delivery splits its time:
//
//   - the first two thirds claim work (claimUntil);
//   - a built-in run starts no new turn in the last sixth and continues in a fresh run (turnsOver,
//     outcome "continue");
//   - a runner run is handed over in the last fifteenth: the job keeps running and the next
//     delivery keeps watching it (handOverAt, handOver, resumeRun).
//
// Anything else that ends a run's context is told apart from a human stop by the cancel cause.

// errStopped is the cancel cause of a run a human stopped: Cancel, or a lease the heartbeat could
// not extend because the task left IN_PROGRESS.
var errStopped = errors.New("stopped by a human")

// stopped is the outcome of a run whose context ended: canceled when a human stopped it, otherwise
// interrupted (the server shut down, or the delivery's request was cut off).
func stopped(ctx context.Context) *outcome {
	if errors.Is(context.Cause(ctx), errStopped) {
		return &outcome{kind: "canceled"}
	}
	return &outcome{kind: "interrupted"}
}

type deliveryKey struct{}

type delivery struct {
	start time.Time
	limit time.Duration
}

func withDelivery(ctx context.Context, start time.Time, limit time.Duration) context.Context {
	return context.WithValue(ctx, deliveryKey{}, delivery{start: start, limit: limit})
}

func claimUntil(start time.Time, limit time.Duration) time.Time { return start.Add(limit * 2 / 3) }

// turnsOver reports whether a built-in run in a delivery should start no new turn.
func turnsOver(ctx context.Context) bool {
	d, ok := ctx.Value(deliveryKey{}).(delivery)
	return ok && time.Now().After(d.start.Add(d.limit-d.limit/6))
}

// handOverAt is when a runner run in a delivery is handed over to the next one.
func handOverAt(ctx context.Context) (time.Time, bool) {
	d, ok := ctx.Value(deliveryKey{}).(delivery)
	return d.start.Add(d.limit - d.limit/15), ok
}

// handOverLease is how long a handed-over task's lease lasts without a delivery watching it: room
// for the queue to deliver, after which the sweep treats the run as interrupted.
const handOverLease = 10 * time.Minute

// handOver leaves a runner run running for the next delivery: the task keeps its lease, the run is
// marked for ClaimHandedOverRuns, and a delivery is queued.
func (c *Crew) handOver(ctx context.Context, task *store.Task, run *store.AgentRun) {
	if _, err := c.store.ExtendTaskLease(ctx, task.ID, run.Attempt, handOverLease); err != nil {
		c.logger.Warn("hand over run: lease", "task", task.ID, "err", err)
	}
	if err := c.store.HandOverAgentRun(ctx, run.ID); err != nil {
		c.logger.Error("hand over run", "task", task.ID, "err", err)
	}
	c.Wake()
}

// resumeRun keeps watching a runner run an earlier delivery handed over.
func (c *Crew) resumeRun(ctx context.Context, runID uuid.UUID) {
	bg := context.WithoutCancel(ctx)
	run, err := c.store.GetAgentRun(bg, runID)
	if err != nil || run.AgentID == nil {
		c.logger.Error("resume run", "run", runID, "err", err)
		return
	}
	task, err1 := c.store.GetTask(bg, run.TaskID)
	a, err2 := c.store.GetAgent(bg, *run.AgentID)
	job, err3 := c.store.RunnerJobForRun(bg, run.ID)
	if err := errors.Join(err1, err2, err3); err != nil {
		run.Status, run.Outcome, run.Error = "FAILED", "error", "could not resume: "+err.Error()
		_ = c.store.FinishAgentRun(bg, run)
		c.logger.Error("resume run", "run", runID, "err", err)
		return
	}
	defer c.taskOrgs.Delete(task.ID)
	if run.Attempt == 0 {
		run.Attempt = task.Attempts // handed over by a version that didn't record the claim
	}
	c.tracked(ctx, task.ID, func(ctx context.Context) {
		ctx, cancelRun := context.WithCancelCause(ctx)
		defer cancelRun(nil)
		if ok, _ := c.store.ExtendTaskLease(bg, task.ID, run.Attempt, c.Lease); !ok {
			cancelRun(errStopped) // a human stopped or moved the task between deliveries, or it was claimed again
		}
		stopBeat := c.heartbeat(ctx, cancelRun, task.ID, run.Attempt)
		defer stopBeat()
		c.endRun(ctx, task, a, run, c.rewatchRunner(ctx, task, a, run, job), stopBeat)
	})
}

// rewatchRunner rebuilds what waitRunnerJob needs for a handed-over job: the pool, and the run's tool
// server from the token in the job (which still holds any terminal call made in between).
func (c *Crew) rewatchRunner(ctx context.Context, task *store.Task, a *store.Agent, run *store.AgentRun, job *store.RunnerJob) *outcome {
	run.ConnectorID, run.Runtime = a.RuntimeConnectorID, "acp"
	if ctx.Err() != nil || a.RuntimeConnectorID == nil {
		c.stopRunnerJob(context.WithoutCancel(ctx), job.ID, 5*time.Second)
		return &outcome{kind: "canceled"}
	}
	inst, _, err := c.hub.Instance(ctx, *a.RuntimeConnectorID, false)
	if err != nil {
		return &outcome{kind: "error", text: "runtime connector: " + err.Error()}
	}
	defer hub.Close(inst)
	pool, ok := inst.(runnerPool)
	if !ok {
		return &outcome{kind: "error", text: "the agent's runtime is no longer a runner pool"}
	}
	var payload runnerapi.Job
	_ = json.Unmarshal(job.Payload, &payload)
	token := strings.TrimPrefix(payload.MCPPath, MCPPathPrefix)
	mrun := c.lookupMCPRun(ctx, token)
	if mrun == nil {
		c.stopRunnerJob(context.WithoutCancel(ctx), job.ID, 5*time.Second)
		return &outcome{kind: "error", text: "the run's tool server could not be restored"}
	}
	go c.watchOutcome(ctx, mrun)
	handedOver := false
	defer func() {
		c.mcp.remove(token)
		if !handedOver {
			_ = c.store.DeleteRunToken(context.WithoutCancel(ctx), token)
		}
		if mrun.env.searcher != nil {
			hub.Close(mrun.env.searcher)
		}
	}()
	out := c.waitRunnerJob(ctx, run, mrun.env, mrun, pool, job)
	handedOver = out.kind == "handover"
	return out
}

// maxContinues caps the fresh runs in a row that continue a built-in run cut short by the delivery;
// the next waits for a human (a runaway agent would otherwise never stop).
const maxContinues = 5

// outcomeContinued is the outcome recorded for a run that continued in a fresh one.
const outcomeContinued = "continued"

// continueRun decides whether a run that used its delivery's time continues on its own, and what
// the thread says either way.
func (c *Crew) continueRun(ctx context.Context, task *store.Task, run *store.AgentRun) (bool, string) {
	const why = "The run reached the time one cloud request allows"
	if run == nil {
		return false, why + ". Reply or press Run to continue."
	}
	n, err := c.streak(ctx, task, run, outcomeContinued)
	if err != nil {
		c.logger.Warn("count continued runs; waiting for the human", "task", task.ID, "err", err)
		return false, why + ". Reply or press Run to continue."
	}
	if n > maxContinues {
		return false, fmt.Sprintf("%s, for the %d%s time in a row, so it waits for you this time. Reply or press Run to continue.", why, n, ordinal(n))
	}
	return true, fmt.Sprintf("%s, so it continues in a fresh run (%d of %d in a row). Comment any time to steer it.", why, n, maxContinues)
}
