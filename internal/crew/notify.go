package crew

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/store"
	"github.com/j75689/cueflow/internal/textutil"
)

// alertTimeout bounds one alert, so a notifier that hangs can't hold up the end of a run.
const alertTimeout = 15 * time.Second

// alertHeadline is the alert's first line for a run that ended with the task waiting for a human
// (WAITING_HUMAN or REVIEW); "" for outcomes that send none.
func alertHeadline(kind string) string {
	switch kind {
	case "complete":
		return "✅ Ready for review"
	case "ask":
		return "❓ Question for you"
	case "approval":
		return "🔐 Needs your approval"
	case "handoff":
		return "⏸ Handed off, waiting for you"
	case "limit":
		return "⚠️ Stopped: turn budget used"
	case "overflow":
		return "⚠️ Stopped: context window full"
	case "interrupted":
		return "⚠️ Interrupted"
	case "error":
		return "❌ Run failed"
	}
	return "" // canceled (a human stopped it) and wait (blocked on subtasks) need no one
}

// alert tells the task's org, through the notifier the org picked, that the task now waits for a
// human. It is best effort: a missing, disabled or failing notifier is logged, never returned.
func (c *Crew) alert(ctx context.Context, task *store.Task, a *store.Agent, kind, detail string) {
	head := alertHeadline(kind)
	if head == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertTimeout)
	defer cancel()
	org, err := c.store.GetOrg(ctx, task.OrgID)
	if err != nil || org.NotifierID == nil {
		return
	}
	n, conn, err := c.hub.Notifier(ctx, *org.NotifierID)
	if err != nil {
		if conn == nil || conn.IsActive { // a disabled notifier is how alerts are muted
			c.logger.Warn("alert notifier", "org", org.Slug, "err", err)
		}
		return
	}
	defer hub.Close(n)

	var b strings.Builder
	b.WriteString(org.Name)
	if a != nil {
		b.WriteString(" · " + a.Name)
	}
	if d := textutil.Clip(detail, 500); d != "" {
		b.WriteString("\n\n" + d)
	}
	if c.PublicURL != "" {
		b.WriteString("\n\n" + strings.TrimRight(c.PublicURL, "/") + "/tasks/view/?id=" + task.ID.String() + "&org=" + url.QueryEscape(org.Slug))
	}
	note := connector.Notification{Subject: head + ": " + textutil.Clip(task.Title, 120), Text: b.String()}
	if err := n.Notify(ctx, note); err != nil {
		c.logger.Warn("alert", "org", org.Slug, "task", task.ID, "err", err)
	}
}
