package crew

import (
	"context"
	"fmt"
	"time"

	"github.com/j75689/cueflow/internal/store"
)

// outcomeRecovered is the outcome recorded for a failed run whose task went back to TODO on its own.
const outcomeRecovered = "retry"

// defaultRecoverBackoff waits 30s before the first recovery and doubles up to 10 minutes.
func defaultRecoverBackoff(k int) time.Duration {
	return min(30*time.Second<<(k-1), 10*time.Minute)
}

// waitText says a backoff the way people do: "30s", "2m".
func waitText(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

// recovery is what autoRecover decided for a failed run.
type recovery struct {
	retry bool          // the task goes back to TODO on its own...
	after time.Duration // ...claimable after this backoff
	// note is the thread's line about it, and kind and alert the one alert sent instead of the plain
	// failed one; all "" when auto-recover doesn't apply.
	note, kind, alert string
}

// autoRecover decides whether a run that failed for a transient reason (out.retry) starts again in a
// fresh run without a human: only for an agent with AutoRecover, and only that many times in a row.
// The streak is read from the task's recorded runs, as autoHandOff's is: it counts the latest runs
// recovered this way ("retry"). A failure that gave up keeps its own outcome and so ends it; so does
// any other outcome, or a human message since a run started.
func (c *Crew) autoRecover(ctx context.Context, task *store.Task, a *store.Agent, run *store.AgentRun, out *outcome) recovery {
	if a == nil || a.AutoRecover == 0 || !out.retry || run == nil {
		return recovery{}
	}
	k, err := c.streak(ctx, task, run, outcomeRecovered)
	if err != nil {
		c.logger.Warn("count recovered runs; waiting for the human", "task", task.ID, "err", err)
		return recovery{}
	}
	n := a.AutoRecover
	if k > n {
		msg := fmt.Sprintf("Auto-recover gave up after %d attempts in a row. Reply or press Run to continue.", n)
		return recovery{note: msg, kind: "recover_gaveup", alert: msg}
	}
	after := c.RecoverBackoff(k)
	kind := "recover"
	if out.kind == "interrupted" {
		kind = "recover_interrupted"
	}
	return recovery{retry: true, after: after, kind: kind,
		note: fmt.Sprintf("Auto-recover %d of %d: a fresh run starts in %s from the task, its thread and files. "+
			"Comment or press Run to retry sooner, or Stop to cancel.", k, n, waitText(after)),
		alert: fmt.Sprintf("Auto-recover %d of %d: retrying in %s.", k, n, waitText(after))}
}
