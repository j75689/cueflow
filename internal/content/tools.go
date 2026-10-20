package content

// The content workflow's agent tools: collecting material from source connectors, writing post
// drafts into the human review queue and illustrating them. They register with the crew at init.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/media"
	"github.com/j75689/cueflow/internal/store"
	"github.com/j75689/cueflow/internal/textutil"
)

const (
	maxDraftsPerRun = 10
)

// contentPrompt is added for agents that draft posts: nothing they write is published until a human
// approves it, and they should say so.
const contentPrompt = `
- You cannot publish to social media - no tool can. Drafts you create go to a human review queue. Say so honestly.
- Name the draft IDs you created in your complete_task summary.`

// registerTools adds the content capabilities, tools and brief section to the crew (see register).
// registerTools adds the sources capability and its tools to the crew (see register).
func registerTools() {
	crew.RegisterCapability(crew.Capability{ID: "sources", Name: "Sources", Default: true,
		Description: "Fetch and search material collected by source connectors (RSS feeds, mailboxes)."})
	for _, t := range contentTools {
		crew.RegisterTool(t)
	}
	crew.RegisterBrief(contentBrief)
}

var contentTools = []crew.ToolSpec{
	{Name: "search_materials", Capability: "sources", Description: "Search items already collected from RSS feeds and mailboxes.",
		Schema: crew.Obj(map[string]any{"query": crew.Str("Keyword (blank = latest)"), "days": crew.Integer("Look back this many days (default 7)"), "limit": crew.Integer("Max items (default 10)")}), Run: toolSearchMaterials, SaveLargeResults: true},
	{Name: "fetch_sources", Capability: "sources", Description: "Pull the latest items from your source connectors (RSS, IMAP) right now.",
		Schema: crew.Obj(map[string]any{}), Run: toolFetchSources},
}

func platformIDs() []string {
	var ids []string
	for _, p := range Platforms() {
		ids = append(ids, p.ID)
	}
	return ids
}

// contentBrief tells the agent which drafts this task already has and where they will be published.
func contentBrief(ctx context.Context, d crew.Deps, task *store.Task) string {
	pt := resolvePublishTarget(ctx, d, task)
	if pt == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Publishing\n")
	name := pt.name
	if name == "" {
		name = "the chosen publisher"
	}
	if pls, ok := pt.platforms(); ok {
		var labels []string
		for _, id := range pt.picked {
			if a, ok := pt.known[id]; ok {
				labels = append(labels, a.Platform+" "+a.Name)
			}
		}
		fmt.Fprintf(&b, "Drafts for this task go to %s: %s. Only write drafts for: %s.\n",
			name, strings.Join(labels, ", "), strings.Join(pls, ", "))
	} else {
		fmt.Fprintf(&b, "Drafts for this task go to %s.\n", name)
	}
	return b.String()
}

// --- sources ---

func toolSearchMaterials(ctx context.Context, r *crew.Run, raw json.RawMessage) (string, error) {
	var a struct {
		Query string
		Days  int
		Limit int
	}
	if err := crew.DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	if a.Days <= 0 || a.Days > 90 {
		a.Days = 7
	}
	items, err := NewStore(r.Store).SearchItems(ctx, r.Agent.SourceConnectorIDs, a.Query, time.Now().AddDate(0, 0, -a.Days), a.Limit)
	if err != nil {
		return "", err
	}
	type row struct {
		Title     string `json:"title"`
		URL       string `json:"url"`
		Author    string `json:"author,omitempty"`
		Published string `json:"published,omitempty"`
		Excerpt   string `json:"excerpt"`
	}
	out := []row{}
	for _, it := range items {
		r := row{Title: it.Title, URL: it.URL, Author: it.Author, Excerpt: textutil.Clip(it.Content, 600)}
		if it.PublishedAt != nil {
			r.Published = it.PublishedAt.Format("2006-01-02")
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return "No matching material. Try fetch_sources first, or web_search.", nil
	}
	return crew.JSONResult(out)
}

func toolFetchSources(ctx context.Context, r *crew.Run, _ json.RawMessage) (string, error) {
	ids := r.Agent.SourceConnectorIDs
	if len(ids) == 0 {
		cs, err := r.Store.ListConnectors(ctx, string(connector.KindSource))
		if err != nil {
			return "", err
		}
		for _, c := range cs {
			if c.IsActive {
				ids = append(ids, c.ID)
			}
		}
	}
	if len(ids) == 0 {
		return "", errors.New("no source connectors configured")
	}
	var lines []string
	for _, id := range ids {
		n, err := FetchSource(ctx, r.Hub, NewStore(r.Store), id, time.Now().AddDate(0, 0, -7), 0)
		name := id.String()
		if c, cerr := r.Store.GetConnector(ctx, id); cerr == nil {
			name = c.Name
		}
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: error: %v", name, err))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %d new items", name, n))
	}
	return strings.Join(lines, "\n"), nil
}

// --- drafting ---

// publishTarget is where a task's drafts go: its publisher and the accounts picked for it.
type publishTarget struct {
	connectorID uuid.UUID
	name        string
	picked      []string                     // account IDs as set on the task
	known       map[string]connector.Account // picked accounts the publisher still lists; nil if unknown
}

// resolvePublishTarget resolves the task's publishing defaults, or nil when it has none. The account list
// comes from the publisher; if it cannot be read, accounts are taken as given.
func resolvePublishTarget(ctx context.Context, d crew.Deps, t *store.Task) *publishTarget {
	if t.PublisherConnectorID == nil {
		return nil
	}
	pt := &publishTarget{connectorID: *t.PublisherConnectorID, picked: t.PublishAccounts}
	if conn, err := d.Store.GetConnector(ctx, pt.connectorID); err == nil {
		pt.name = conn.Name
	}
	if len(pt.picked) == 0 {
		return pt
	}
	inst, _, err := d.Hub.Instance(ctx, pt.connectorID, true)
	if err != nil {
		return pt
	}
	defer hub.Close(inst)
	al, ok := inst.(connector.AccountLister)
	if !ok {
		return pt
	}
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	list, err := al.ListAccounts(lctx)
	if err != nil {
		d.Logger.Warn("list publisher accounts", "err", err)
		return pt
	}
	pt.known = map[string]connector.Account{}
	for _, a := range list {
		if slices.Contains(pt.picked, a.ID) {
			pt.known[a.ID] = a
		}
	}
	return pt
}

// platforms lists the platforms the picked accounts are on; ok is false when that is unknown
// (no accounts picked, or the publisher's list could not be read).
func (pt *publishTarget) platforms() ([]string, bool) {
	if pt == nil || len(pt.picked) == 0 || pt.known == nil {
		return nil, false
	}
	var out []string
	for _, id := range pt.picked {
		if a, ok := pt.known[id]; ok && !slices.Contains(out, a.Platform) {
			out = append(out, a.Platform)
		}
	}
	return out, true
}

// accountsOn returns the picked accounts on platform (all of them if their platforms are unknown).
func (pt *publishTarget) accountsOn(platform string) []string {
	if pt == nil {
		return nil
	}
	if pt.known == nil {
		return pt.picked
	}
	var out []string
	for _, id := range pt.picked {
		if a, ok := pt.known[id]; ok && a.Platform == platform {
			out = append(out, id)
		}
	}
	return out
}

// cleanMediaURLs trims the list and accepts only CueFlow media URLs (/media/...) and public https URLs.
func cleanMediaURLs(in []string) ([]string, error) {
	var out []string
	for _, u := range in {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if _, stored := media.IDFromURL(u); !(stored && strings.HasPrefix(u, media.PathPrefix)) && !strings.HasPrefix(u, "https://") {
			return nil, fmt.Errorf("media_urls must be CueFlow media URLs (/media/...) or https URLs, got %q", u)
		}
		out = append(out, u)
	}
	return out, nil
}
