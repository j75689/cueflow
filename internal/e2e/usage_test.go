package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/j75689/cueflow/internal/store"
)

// The usage report counts a run on the day it started in the caller's time zone, on every backend: a
// run a minute before local midnight and one a minute after land on two days, though both are on the
// same day in UTC.
func TestUsageBucketsDaysInTheZone(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	def, _ := e.store.DefaultOrg(ctx)
	task, err := e.store.CreateTask(ctx, store.NewTask{OrgID: def.ID, Title: "Counted"})
	if err != nil {
		t.Fatal(err)
	}
	taipei, _ := time.LoadLocation("Asia/Taipei") // UTC+8: local midnight is 16:00 UTC the day before
	now := time.Now().In(taipei)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, taipei).AddDate(0, 0, -1)
	for _, run := range []struct {
		at     time.Time
		tokens int
	}{{midnight.Add(-time.Minute), 100}, {midnight.Add(time.Minute), 20}, {midnight.Add(2 * time.Minute), 3}} {
		if err := e.db.Insert(ctx, "agent_runs", map[string]any{"task_id": task.ID, "org_id": def.ID, "status": "SUCCEEDED",
			"input_tokens": run.tokens, "started_at": run.at}); err != nil {
			t.Fatal(err)
		}
	}

	var usage store.Usage
	if code := e.do("GET", "/usage?days=3&tz=Asia/Taipei", nil, &usage); code != 200 {
		t.Fatalf("usage: %d", code)
	}
	want := []store.UsageDay{
		{Date: midnight.AddDate(0, 0, -1).Format("2006-01-02"), Runs: 1, InputTokens: 100},
		{Date: midnight.Format("2006-01-02"), Runs: 2, InputTokens: 23},
		{Date: now.Format("2006-01-02")},
	}
	if len(usage.Daily) != len(want) {
		t.Fatalf("daily: %+v, want %+v", usage.Daily, want)
	}
	for i := range want {
		if usage.Daily[i] != want[i] {
			t.Fatalf("daily: %+v, want %+v", usage.Daily, want)
		}
	}
	if usage.Totals.Runs != 3 || usage.Totals.InputTokens != 123 {
		t.Fatalf("totals: %+v", usage.Totals)
	}
}
