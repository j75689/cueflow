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
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/media"
	"github.com/j75689/cueflow/internal/store"
	"github.com/j75689/cueflow/internal/textutil"
)

const (
	maxDraftsPerRun = 10
	maxImagesPerRun = 2
)

// contentPrompt is added for agents that draft posts: nothing they write is published until a human
// approves it, and they should say so.
const contentPrompt = `
- You cannot publish to social media - no tool can. Drafts you create go to a human review queue. Say so honestly.
- Name the draft IDs you created in your complete_task summary.`

// registerTools adds the content capabilities, tools and brief section to the crew (see register).
func registerTools() {
	crew.RegisterCapability(crew.Capability{ID: "sources", Name: "Sources", Default: true,
		Description: "Fetch and search material collected by source connectors (RSS feeds, mailboxes)."})
	crew.RegisterCapability(crew.Capability{ID: "drafting", Name: "Drafting", Default: true,
		Description: "Write social post drafts into the human review queue and revise drafts that are still pending.",
		Prompt: func(a *store.Agent) string {
			p := contentPrompt
			if len(a.Platforms) > 0 {
				p += fmt.Sprintf("\n- Unless told otherwise, write drafts for: %s.", strings.Join(a.Platforms, ", "))
			}
			return p
		}})
	crew.RegisterCapability(crew.Capability{ID: "imaging", Name: "Images",
		Description: "Generate images for drafts with the image connector (each image costs money; at most a few per run)."})
	for _, t := range contentTools {
		crew.RegisterTool(t)
	}
	crew.RegisterBrief(contentBrief)
}

var contentTools = []crew.ToolSpec{
	{Name: "search_materials", Capability: "sources", Description: "Search items already collected from RSS feeds and mailboxes.",
		Schema: crew.Obj(map[string]any{"query": crew.Str("Keyword (blank = latest)"), "days": crew.Integer("Look back this many days (default 7)"), "limit": crew.Integer("Max items (default 10)")}), Run: toolSearchMaterials},
	{Name: "fetch_sources", Capability: "sources", Description: "Pull the latest items from your source connectors (RSS, IMAP) right now.",
		Schema: crew.Obj(map[string]any{}), Run: toolFetchSources},

	// --- drafting ---
	{Name: "create_post_draft", Capability: "drafting", Description: "Create one social post draft for one platform. It goes to the human review queue; it is NOT published.",
		Schema: crew.Obj(map[string]any{
			"platform":     map[string]any{"type": "string", "enum": platformIDs()},
			"title":        crew.Str("Short internal title for the reviewer"),
			"content":      crew.Str("Post body, plain text, no hashtags"),
			"thread_parts": crew.StrList("The whole thread in order, first part included, for thread-capable platforms (optional; content is then the first part)"),
			"hashtags":     crew.StrList("Hashtags without '#'"),
			"image_prompt": crew.Str("Prompt for an accompanying image (Instagram)"),
			"topic":        crew.Str("Threads only: one topic tag, up to 50 characters, no '.' or '&' (optional)"),
			"link":         crew.Str("Optional URL shown as a link preview card (Threads); defaults to the first source_urls entry"),
			"publish_at":   crew.Str("Optional RFC 3339 time to publish once approved, e.g. 2026-10-07T12:00:00+08:00 (must be in the future)"),
			"media_urls":   crew.StrList("Optional images or videos to attach: CueFlow media URLs (/media/...) returned by tools, or public https URLs"),
			"source_urls":  crew.StrList("URLs backing the claims in this post"),
		}, "platform", "content"), Run: toolCreateDraft},
	{Name: "list_posts", Capability: "drafting", Description: "List recent posts and drafts with their review status.",
		Schema: crew.Obj(map[string]any{"status": crew.Str("Optional status filter, e.g. PENDING_REVIEW"), "limit": crew.Integer("Max posts (default 10)")}), Run: toolListPosts},
	{Name: "revise_draft", Capability: "drafting", Description: "Revise a draft that is still DRAFT or PENDING_REVIEW.",
		Schema: crew.Obj(map[string]any{
			"post_id": crew.Str("Post ID"), "title": crew.Str("New title"), "content": crew.Str("New body"),
			"thread_parts": crew.StrList("New thread parts"), "hashtags": crew.StrList("New hashtags"),
			"image_prompt": crew.Str("New image prompt"), "topic": crew.Str("New Threads topic tag (empty removes it)"),
			"link": crew.Str("New link card URL (empty removes it)"), "note": crew.Str("Why you changed it"),
			"media_urls": crew.StrList("Replace the attached images or videos (CueFlow media URLs like /media/... returned by tools, or public https URLs); an empty list removes them"),
		}, "post_id"), Run: toolReviseDraft},

	// --- imaging ---
	{Name: "generate_image", Capability: "imaging", Description: fmt.Sprintf("Generate an image for a draft that is still DRAFT or PENDING_REVIEW and attach it. Uses the draft's image_prompt unless you give a prompt. Costs money: at most %d per run.", maxImagesPerRun),
		Schema: crew.Obj(map[string]any{
			"post_id": crew.Str("Draft (post) ID"),
			"prompt":  crew.Str("Image prompt in English: subject, style, composition, no text in the image (optional; defaults to the draft's image_prompt)"),
			"aspect":  map[string]any{"type": "string", "enum": connector.Aspects, "description": "Aspect ratio (default 4:5 for Instagram, else 1:1)"},
		}, "post_id"), Run: toolGenerateImage},
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
	var b strings.Builder
	if posts, err := NewStore(d.Store).PostsForTask(ctx, task.ID); err == nil && len(posts) > 0 {
		b.WriteString("\n## Drafts created for this task\n")
		for _, p := range posts {
			fmt.Fprintf(&b, "- %s [%s] %s: %s\n", p.ID, p.Status, strings.Join(p.Platforms, ","), p.Title)
		}
	}
	if pt := resolvePublishTarget(ctx, d, task); pt != nil {
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

func toolCreateDraft(ctx context.Context, r *crew.Run, raw json.RawMessage) (string, error) {
	var a struct {
		Platform    string   `json:"platform"`
		Title       string   `json:"title"`
		Content     string   `json:"content"`
		ThreadParts []string `json:"thread_parts"`
		Hashtags    []string `json:"hashtags"`
		ImagePrompt string   `json:"image_prompt"`
		Topic       string   `json:"topic"`
		Link        string   `json:"link"`
		PublishAt   string   `json:"publish_at"`
		MediaURLs   []string `json:"media_urls"`
		SourceURLs  []string `json:"source_urls"`
	}
	if err := crew.DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	if r.Count("drafts") >= maxDraftsPerRun {
		return "", fmt.Errorf("draft limit reached (%d per run)", maxDraftsPerRun)
	}
	if a.Platform == "threads" && strings.TrimSpace(a.Link) == "" && len(a.SourceURLs) > 0 {
		// No link card given: show the main source, so readers can always open it (Threads only).
		a.Link = strings.TrimSpace(a.SourceURLs[0])
	}
	mediaURLs, err := cleanMediaURLs(a.MediaURLs)
	if err != nil {
		return "", err
	}
	var publishAt *time.Time
	if s := strings.TrimSpace(a.PublishAt); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return "", fmt.Errorf("publish_at must be RFC 3339, e.g. 2026-10-07T12:00:00+08:00: %v", err)
		}
		if !t.After(time.Now()) {
			return "", errors.New("publish_at must be in the future")
		}
		publishAt = &t
	}
	if _, ok := PlatformByID(a.Platform); !ok {
		return "", fmt.Errorf("unknown platform %q (use one of %s)", a.Platform, strings.Join(platformIDs(), ", "))
	}
	drafts := Normalize([]Draft{{Platform: a.Platform, Title: a.Title, Content: a.Content,
		ThreadParts: a.ThreadParts, Hashtags: a.Hashtags, ImagePrompt: a.ImagePrompt}}, []string{a.Platform})
	if len(drafts) == 0 {
		return "", errors.New("content is empty")
	}
	d := drafts[0]
	taskID := r.Task.ID
	var publisher *uuid.UUID
	var accounts []string
	if pt := resolvePublishTarget(ctx, r.Deps, r.Task); pt != nil {
		if pls, ok := pt.platforms(); ok && !slices.Contains(pls, d.Platform) {
			return "", fmt.Errorf("this task publishes only to %s (the accounts chosen for it); do not write a %s draft",
				strings.Join(pls, ", "), d.Platform)
		}
		publisher, accounts = &pt.connectorID, pt.accountsOn(d.Platform)
	}
	post, err := NewStore(r.Store).CreatePost(ctx, NewPost{
		TaskID: &taskID, Title: d.Title, Content: d.Content, ThreadParts: d.ThreadParts, Hashtags: d.Hashtags,
		ImagePrompt: d.ImagePrompt, Platforms: []string{d.Platform}, Topic: a.Topic, LinkURL: a.Link,
		PublisherConnectorID: publisher, Accounts: accounts, ScheduledAt: publishAt, MediaURLs: mediaURLs,
		SourceContext: map[string]any{"task": r.Task.Number, "agent": r.Agent.Name, "urls": a.SourceURLs},
	}, ActorAgent, true)
	if err != nil {
		return "", err
	}
	r.Inc("drafts")
	r.Broker.Publish("post.updated", map[string]any{"id": post.ID, "status": post.Status})
	msg := fmt.Sprintf("Draft %s created for %s and queued for human review (not published).", post.ID, d.Platform)
	if len(post.Warnings) > 0 {
		msg += " Warnings: " + strings.Join(post.Warnings, "; ")
	}
	return msg, nil
}

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

// postInOrg loads a post of the task's org; other orgs' posts don't exist for the agent.
func postInOrg(ctx context.Context, r *crew.Run, id uuid.UUID) (*Post, error) {
	p, err := NewStore(r.Store).GetPost(ctx, id)
	if err == nil && p.OrgID != r.Task.OrgID {
		err = domain.ErrNotFound
	}
	return p, err
}

func toolListPosts(ctx context.Context, r *crew.Run, raw json.RawMessage) (string, error) {
	var a struct {
		Status string
		Limit  int
	}
	if err := crew.DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	f := PostFilter{OrgID: &r.Task.OrgID, Limit: a.Limit}
	if f.Limit <= 0 || f.Limit > 30 {
		f.Limit = 10
	}
	if st := Status(strings.ToUpper(strings.TrimSpace(a.Status))); st != "" {
		if !st.Valid() {
			return "", fmt.Errorf("unknown status %q", a.Status)
		}
		f.Statuses = []Status{st}
	}
	posts, _, err := NewStore(r.Store).ListPosts(ctx, f)
	if err != nil {
		return "", err
	}
	type row struct {
		ID        uuid.UUID `json:"id"`
		Status    Status    `json:"status"`
		Platforms []string  `json:"platforms"`
		Title     string    `json:"title"`
		Excerpt   string    `json:"excerpt"`
		Warnings  []string  `json:"warnings,omitempty"`
	}
	out := []row{}
	for _, p := range posts {
		out = append(out, row{p.ID, p.Status, p.Platforms, p.Title, textutil.Clip(p.Content, 240), p.Warnings})
	}
	return crew.JSONResult(out)
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

func toolReviseDraft(ctx context.Context, r *crew.Run, raw json.RawMessage) (string, error) {
	var a struct {
		PostID      string    `json:"post_id"`
		Title       *string   `json:"title"`
		Content     *string   `json:"content"`
		ThreadParts *[]string `json:"thread_parts"`
		Hashtags    *[]string `json:"hashtags"`
		ImagePrompt *string   `json:"image_prompt"`
		Topic       *string   `json:"topic"`
		Link        *string   `json:"link"`
		MediaURLs   *[]string `json:"media_urls"`
		Note        string    `json:"note"`
	}
	if err := crew.DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	id, err := uuid.Parse(strings.TrimSpace(a.PostID))
	if err != nil {
		return "", errors.New("post_id must be a post UUID")
	}
	cur, err := postInOrg(ctx, r, id)
	if err != nil {
		return "", err
	}
	if !cur.Status.Editable() {
		return "", fmt.Errorf("post is %s and can no longer be edited", cur.Status)
	}
	var mediaPatch *[]string
	if a.MediaURLs != nil {
		urls, err := cleanMediaURLs(*a.MediaURLs)
		if err != nil {
			return "", err
		}
		mediaPatch = &urls
	}
	note := fmt.Sprintf("revised by @%s", r.Agent.Slug)
	if a.Note != "" {
		note += ": " + a.Note
	}
	p, err := NewStore(r.Store).UpdatePostAs(ctx, id, cur.Version, PostPatch{Title: a.Title, Content: a.Content,
		ThreadParts: a.ThreadParts, Hashtags: a.Hashtags, ImagePrompt: a.ImagePrompt, Topic: a.Topic, LinkURL: a.Link,
		MediaURLs: mediaPatch}, ActorAgent, note)
	if err != nil {
		return "", err
	}
	r.Broker.Publish("post.updated", map[string]any{"id": p.ID, "status": p.Status})
	msg := "Draft revised."
	if len(p.Warnings) > 0 {
		msg += " Warnings: " + strings.Join(p.Warnings, "; ")
	}
	return msg, nil
}

// --- imaging ---

func toolGenerateImage(ctx context.Context, r *crew.Run, raw json.RawMessage) (string, error) {
	var a struct {
		PostID string `json:"post_id"`
		Prompt string `json:"prompt"`
		Aspect string `json:"aspect"`
	}
	if err := crew.DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	if r.Count("images") >= maxImagesPerRun {
		return "", fmt.Errorf("image limit reached (%d per run)", maxImagesPerRun)
	}
	id, err := uuid.Parse(strings.TrimSpace(a.PostID))
	if err != nil {
		return "", errors.New("post_id must be a post UUID")
	}
	post, err := postInOrg(ctx, r, id)
	if err != nil {
		return "", err
	}
	if !post.Status.Editable() {
		return "", fmt.Errorf("post is %s and can no longer be edited", post.Status)
	}
	prompt := strings.TrimSpace(a.Prompt)
	if prompt == "" {
		prompt = post.ImagePrompt
	}
	aspect := a.Aspect
	if aspect == "" {
		aspect = "1:1"
		if slices.Contains(post.Platforms, "instagram") {
			aspect = "4:5"
		}
	}
	r.Inc("images") // count attempts too: a failed generation may still be billed
	m, err := (&media.Service{Store: r.Store, Hub: r.Hub}).Generate(ctx, media.GenerateRequest{
		Prompt: prompt, Aspect: aspect, CreatedBy: r.Agent.Slug})
	if err != nil {
		return "", err
	}
	urls := append(append([]string{}, post.MediaURLs...), media.URL(m.ID))
	p, err := NewStore(r.Store).UpdatePostAs(ctx, id, post.Version, PostPatch{MediaURLs: &urls}, ActorAgent,
		fmt.Sprintf("image generated by @%s", r.Agent.Slug))
	if err != nil {
		return "", err
	}
	r.Broker.Publish("post.updated", map[string]any{"id": p.ID, "status": p.Status})
	return fmt.Sprintf("Image %s generated (%s) and attached to draft %s. The reviewer sees it with the draft.", media.URL(m.ID), aspect, p.ID), nil
}
