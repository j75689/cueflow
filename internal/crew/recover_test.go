package crew

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/j75689/cueflow/internal/acpdrive"
	"github.com/j75689/cueflow/internal/store"
)

func TestDefaultRecoverBackoff(t *testing.T) {
	for k, want := range map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 4: 4 * time.Minute,
		5: 8 * time.Minute, 6: 10 * time.Minute, 10: 10 * time.Minute} {
		if got := defaultRecoverBackoff(k); got != want {
			t.Errorf("backoff(%d) = %s, want %s", k, got, want)
		}
	}
	for d, want := range map[time.Duration]string{0: "0s", 30 * time.Second: "30s", 2 * time.Minute: "2m", 90 * time.Second: "1m30s"} {
		if got := waitText(d); got != want {
			t.Errorf("waitText(%s) = %q, want %q", d, got, want)
		}
	}
}

// Only failures that may pass on their own are retried: an opaque runner or ACP error is, an external
// agent that ran out of time or refused is not.
func TestACPOutcomeRetry(t *testing.T) {
	for _, tc := range []struct {
		res   acpdrive.Result
		retry bool
	}{
		{acpdrive.Result{Err: errors.New("agent process exited")}, true},
		{acpdrive.Result{TimedOut: true}, false},
		{acpdrive.Result{StopReason: "refusal"}, false},
	} {
		out := (&Crew{}).acpOutcome(context.Background(), &store.AgentRun{}, &runEnv{}, tc.res, time.Minute)
		if out.kind != "error" || out.retry != tc.retry {
			t.Errorf("%+v: %s retry=%v, want error retry=%v", tc.res, out.kind, out.retry, tc.retry)
		}
	}
}

func TestRecoverAlertHeadlines(t *testing.T) {
	for kind, want := range map[string]string{"recover": "❌ Run failed, auto-recovering",
		"recover_interrupted": "⚠️ Interrupted, auto-recovering", "recover_gaveup": "🛑 Run failed, auto-recover gave up"} {
		if got := alertHeadline(kind); got != want {
			t.Errorf("alertHeadline(%q) = %q, want %q", kind, got, want)
		}
	}
}
