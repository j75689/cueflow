package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/acpdrive"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/runnerapi"
	"github.com/j75689/cueflow/internal/store"
)

// runnerPool is what the runner_pool connector exposes (see connectors/runnerpool).
type runnerPool interface {
	Labels() []string
	Preset() string
	Model() string
	RunTimeout() time.Duration
	QueueTimeout() time.Duration
	AllowWeb() bool
}

const runnerPoll = time.Second

// runnerLoop runs one task on a registered runner. The tool server, thread and outcomes are the
// same as acpLoop's; only the agent process lives elsewhere. It queues a job, then waits for the
// runner to finish it, for a terminal tool call, for cancellation, or for the job to be lost.
func (c *Crew) runnerLoop(ctx context.Context, task *store.Task, a *store.Agent, run *store.AgentRun, pool runnerPool) *outcome {
	env := &runEnv{c: c, task: task, agent: a, runID: &run.ID, stop: runStop(ctx)}
	token, mrun, unregister := c.registerMCPRun(ctx, env)
	handedOver := false
	defer func() {
		if handedOver {
			c.mcp.remove(token) // the token stays valid: the runner keeps calling tools while the next delivery watches
		} else {
			unregister()
		}
	}()
	defer func() {
		if env.searcher != nil {
			hub.Close(env.searcher)
		}
	}()

	team, _ := c.store.ListAgents(ctx, task.OrgID)
	var memories []store.AgentMemory
	if a.Has("memory") {
		memories, _ = c.store.Memories(ctx, a.ID, "", 30)
	}
	prompt := acpPrompt + buildSystemPrompt(a, task, team, memories) + "\n\n---\n\n" + c.buildBrief(ctx, task)

	timeout := runnerTimeout(pool)
	job, err := c.store.CreateRunnerJob(ctx, store.NewRunnerJob{
		OrgID: task.OrgID, TaskID: task.ID, AgentRunID: &run.ID, Labels: pool.Labels(),
		Payload: runnerapi.Job{
			TaskID: task.ID.String(), Title: task.Title, Preset: pool.Preset(), Model: pool.Model(), Prompt: prompt,
			MCPPath: MCPPathPrefix + token, TimeoutSeconds: int(timeout.Seconds()), AllowWeb: pool.AllowWeb(),
		},
	})
	if err != nil {
		return &outcome{kind: "error", text: "could not queue the runner job: " + err.Error()}
	}
	run.ConnectorID, run.Runtime = a.RuntimeConnectorID, "acp"
	c.system(ctx, task.ID, fmt.Sprintf("Waiting for a runner (%s).", runnerLabels(pool)))
	out := c.waitRunnerJob(ctx, run, env, mrun, pool, job)
	handedOver = out.kind == "handover"
	return out
}

func runnerTimeout(pool runnerPool) time.Duration {
	if t := pool.RunTimeout(); t > 0 {
		return t
	}
	return 20 * time.Minute
}

func runnerLabels(pool runnerPool) string {
	if l := pool.Labels(); len(l) > 0 {
		return "labels: " + strings.Join(l, ", ")
	}
	return "any runner"
}

// waitRunnerJob waits for the runner to finish the job, for a terminal tool call, for cancellation,
// for the job to be lost, or (in a cloud delivery) for the delivery's time to be nearly up: then it
// returns "handover" and leaves the job running for the next delivery to keep watching (resumeRun).
func (c *Crew) waitRunnerJob(ctx context.Context, run *store.AgentRun, env *runEnv, mrun *mcpRun, pool runnerPool, job *store.RunnerJob) *outcome {
	timeout := runnerTimeout(pool)
	// A little slack beyond the agent's own timeout covers startup and the final upload. Both
	// deadlines count from the job's creation, so they hold across deliveries.
	deadline := job.CreatedAt.Add(timeout + 2*time.Minute)
	queueDeadline := job.CreatedAt.Add(pool.QueueTimeout())
	var handOver <-chan time.Time
	if at, ok := handOverAt(ctx); ok {
		hand := time.NewTimer(time.Until(at))
		defer hand.Stop()
		handOver = hand.C
	}
	t := time.NewTicker(runnerPoll)
	defer t.Stop()
	var res acpdrive.Result
	finished := false
	for !finished {
		select {
		case <-ctx.Done():
			c.stopRunnerJob(context.WithoutCancel(ctx), job.ID, 5*time.Second)
			return &outcome{kind: "canceled"}
		case <-mrun.done:
			// A terminal tool (complete_task / ask_human / ...) ended the task; the runner stops its
			// agent and reports the tail of the conversation before we move on.
			c.stopRunnerJob(ctx, job.ID, 20*time.Second)
			return c.acpOutcome(ctx, run, env, acpdrive.Result{}, timeout)
		case <-handOver:
			return &outcome{kind: "handover"}
		case <-t.C:
		}
		cur, err := c.store.GetRunnerJob(ctx, job.ID)
		if err != nil {
			return &outcome{kind: "error", text: "runner job lost: " + err.Error()}
		}
		now := time.Now()
		switch cur.Status {
		case store.JobQueued:
			if now.After(queueDeadline) {
				_, _ = c.store.EndRunnerJob(ctx, job.ID, store.JobExpired, nil)
				return &outcome{kind: "error", text: fmt.Sprintf("no runner picked up the work within %s (%s)", pool.QueueTimeout(), runnerLabels(pool))}
			}
		case store.JobRunning:
			if cur.LeaseUntil != nil && now.After(*cur.LeaseUntil) || now.After(deadline) {
				_, _ = c.store.EndRunnerJob(ctx, job.ID, store.JobExpired, nil)
				return &outcome{kind: "error", text: fmt.Sprintf("runner %q stopped responding", cur.RunnerName)}
			}
		case store.JobCanceling:
			// Only we set this; the runner is winding down.
		default:
			var cr runnerapi.CompleteRequest
			if len(cur.Result) > 0 {
				_ = json.Unmarshal(cur.Result, &cr)
			}
			res = acpdrive.Result{
				AgentName: cr.AgentName, StopReason: acp.StopReason(cr.StopReason), FinalText: cr.FinalText,
				ToolCalls: cr.ToolCalls, TimedOut: cr.TimedOut, Canceled: cr.Canceled,
				Usage: &acp.Usage{InputTokens: cr.InputTokens, OutputTokens: cr.OutputTokens},
			}
			if cr.Error != "" {
				res.Err = errors.New(cr.Error)
			} else if cur.Status != store.JobDone {
				res.Err = fmt.Errorf("the runner job ended as %s", cur.Status)
			}
			finished = true
		}
	}
	return c.acpOutcome(ctx, run, env, res, timeout)
}

// ApplyRunnerEvents writes what a runner's agent did into the task thread.
func (c *Crew) ApplyRunnerEvents(ctx context.Context, job *store.RunnerJob, events []runnerapi.Event) {
	var agent *store.Agent
	if t, err := c.store.GetTask(ctx, job.TaskID); err == nil && t.AssigneeAgentID != nil {
		agent, _ = c.store.GetAgent(ctx, *t.AssigneeAgentID)
	}
	sink := crewSink{c: c, task: &store.Task{ID: job.TaskID}, agent: agent}
	for _, e := range events {
		content := clip(e.Content, 8000)
		switch e.Kind {
		case "message", "tool_call", "tool_result":
			sink.Say(ctx, e.Kind, content, e.Data)
		case "system":
			sink.System(ctx, content)
		}
	}
}

// stopRunnerJob asks the runner to stop and waits up to grace for it to report; whatever is
// still open after that is closed by force.
func (c *Crew) stopRunnerJob(ctx context.Context, id uuid.UUID, grace time.Duration) {
	_ = c.store.StopRunnerJob(ctx, id)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if j, err := c.store.GetRunnerJob(ctx, id); err != nil || store.JobFinished(j.Status) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	_, _ = c.store.EndRunnerJob(ctx, id, store.JobCanceled, nil)
}

// RunnerPickedUp notes in the task thread which runner took the work.
func (c *Crew) RunnerPickedUp(ctx context.Context, taskID uuid.UUID, runnerName string) {
	c.system(ctx, taskID, fmt.Sprintf("Picked up by runner %q.", runnerName))
	c.broker.Publish(c.taskOrg(ctx, taskID), "task.updated", map[string]any{"id": taskID})
}
