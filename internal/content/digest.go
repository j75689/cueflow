package content

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/hub"
)

// sendDigest mails the posts awaiting review through every active notifier.
func (m *Module) sendDigest(ctx context.Context) {
	posts, err := m.Store.PendingReview(ctx, 50)
	if err != nil || len(posts) == 0 {
		return
	}
	notifiers, err := m.Store.ListConnectors(ctx, string(connector.KindNotifier))
	if err != nil {
		return
	}
	note := ReviewDigest(strings.TrimRight(m.PublicURL, "/"),
		fmt.Sprintf("[CueFlow] %d drafts awaiting review", len(posts)), posts)
	for _, c := range notifiers {
		if !c.IsActive {
			continue
		}
		n, _, err := m.Hub.Notifier(ctx, c.ID)
		if err != nil {
			m.Logger.Warn("digest notifier", "connector", c.Name, "err", err)
			continue
		}
		if err := n.Notify(ctx, note); err != nil {
			m.Logger.Warn("digest send", "connector", c.Name, "err", err)
		}
		hub.Close(n)
	}
}

// ReviewDigest renders a review reminder listing posts with links into the dashboard.
func ReviewDigest(publicURL, subject string, posts []*Post) connector.Notification {
	var text, h strings.Builder
	text.WriteString(subject + "\n\n")
	h.WriteString("<h2>" + html.EscapeString(subject) + "</h2><ul>")
	for _, p := range posts {
		link := fmt.Sprintf("%s/posts/view/?id=%s", publicURL, p.ID)
		title := p.Title
		if title == "" {
			title = strings.Join(strings.Fields(p.Content), " ")
		}
		if r := []rune(title); len(r) > 80 {
			title = string(r[:80]) + "…"
		}
		fmt.Fprintf(&text, "- [%s] %s\n  %s\n", strings.Join(p.Platforms, ","), title, link)
		fmt.Fprintf(&h, `<li><b>%s</b> %s — <a href="%s">review</a></li>`,
			html.EscapeString(strings.Join(p.Platforms, ",")), html.EscapeString(title), html.EscapeString(link))
	}
	h.WriteString("</ul>")
	fmt.Fprintf(&text, "\nOpen the review inbox: %s/\n", publicURL)
	fmt.Fprintf(&h, `<p><a href="%s/">Open the review inbox</a></p>`, html.EscapeString(publicURL))
	return connector.Notification{Subject: subject, Text: text.String(), HTML: h.String()}
}
