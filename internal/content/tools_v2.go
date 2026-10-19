package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/media"
	"github.com/j75689/cueflow/internal/store"
	"github.com/j75689/cueflow/internal/textutil"
)

// Drafting: create_post_draft, list_posts and revise_draft write and read post deliverables. The
// capability keeps the id post_deliverables it had while the posts table was still in use, so the
// agents that were moved over keep working.

// PostDeliverablesCapability is the capability id.
const PostDeliverablesCapability = "post_deliverables"

func registerPostTools() {
	crew.RegisterCapability(crew.Capability{ID: PostDeliverablesCapability, Name: "Drafting", Default: true,
		Description: "Write social post drafts into the human review queue (Deliverables) and revise them while they are pending.",
		Prompt: func(a *store.Agent) string {
			p := contentPrompt
			if len(a.Platforms) > 0 {
				p += fmt.Sprintf("\n- Unless told otherwise, write drafts for: %s.", strings.Join(a.Platforms, ", "))
			}
			return p
		}})
	crew.RegisterCapability(crew.Capability{ID: "imaging", Name: "Images",
		Description: "Generate images for post drafts with the image connector (each image costs money; at most a few per run)."})
	for _, t := range postTools {
		crew.RegisterTool(t)
	}
}

const maxImagesPerRun = 2

var postTools = []crew.ToolSpec{
	{Name: "create_post_draft", Capability: PostDeliverablesCapability, Description: "Create one social post draft for one platform. It goes to the human review queue; it is NOT published.",
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
		}, "platform", "content"), Run: toolCreatePostDeliverable},
	{Name: "list_posts", Capability: PostDeliverablesCapability, Description: "List recent posts and drafts with their review status.",
		Schema: crew.Obj(map[string]any{"status": crew.Str("Optional status filter, e.g. PENDING_REVIEW"), "limit": crew.Integer("Max posts (default 10)")}), Run: toolListPostDeliverables},
	{Name: "revise_draft", Capability: PostDeliverablesCapability, Description: "Revise a draft that is still DRAFT or PENDING_REVIEW.",
		Schema: crew.Obj(map[string]any{
			"post_id": crew.Str("Post ID"), "title": crew.Str("New title"), "content": crew.Str("New body"),
			"thread_parts": crew.StrList("New thread parts"), "hashtags": crew.StrList("New hashtags"),
			"image_prompt": crew.Str("New image prompt"), "topic": crew.Str("New Threads topic tag (empty removes it)"),
			"link": crew.Str("New link card URL (empty removes it)"), "note": crew.Str("Why you changed it"),
			"media_urls": crew.StrList("Replace the attached images or videos (CueFlow media URLs like /media/... returned by tools, or public https URLs); an empty list removes them"),
		}, "post_id"), Run: toolRevisePostDeliverable},
	{Name: "generate_image", Capability: "imaging", Description: fmt.Sprintf("Generate an image for a post draft that is still DRAFT or PENDING_REVIEW and attach it. Uses the draft's image_prompt unless you give a prompt. Costs money: at most %d per run.", maxImagesPerRun),
		Schema: crew.Obj(map[string]any{
			"post_id": crew.Str("Draft (post) ID"),
			"prompt":  crew.Str("Image prompt in English: subject, style, composition, no text in the image (optional; defaults to the draft's image_prompt)"),
			"aspect":  map[string]any{"type": "string", "enum": connector.Aspects, "description": "Aspect ratio (default 4:5 for Instagram, else 1:1)"},
		}, "post_id"), Run: toolGenerateImage},
}

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
	ds := deliverable.NewStore(r.Store)
	cur, err := ds.Get(ctx, id)
	if err == nil && (cur.OrgID != r.Task.OrgID || cur.Kind != PostKind) {
		err = domain.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !cur.Status.Editable() {
		return "", fmt.Errorf("post is %s and can no longer be edited", cur.Status)
	}
	p, err := DecodePostPayload(cur.Payload)
	if err != nil {
		return "", err
	}
	prompt := strings.TrimSpace(a.Prompt)
	if prompt == "" {
		prompt = p.ImagePrompt
	}
	aspect := a.Aspect
	if aspect == "" {
		aspect = "1:1"
		if slices.Contains(p.Platforms, "instagram") {
			aspect = "4:5"
		}
	}
	r.Inc("images") // count attempts too: a failed generation may still be billed
	m, err := (&media.Service{Store: r.Store, Hub: r.Hub}).Generate(ctx, media.GenerateRequest{Prompt: prompt, Aspect: aspect, CreatedBy: r.Agent.Slug})
	if err != nil {
		return "", err
	}
	p.MediaURLs = append(p.MediaURLs, media.URL(m.ID))
	rawPayload, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	item, err := ds.Update(ctx, id, cur.Version, deliverable.Patch{Payload: rawPayload}, deliverable.ActorAgent, fmt.Sprintf("image generated by @%s", r.Agent.Slug))
	if err != nil {
		return "", err
	}
	r.Broker.Publish(item.OrgID, "deliverable.updated", map[string]any{"id": item.ID, "status": item.Status})
	return fmt.Sprintf("Image %s generated (%s) and attached to draft %s. The reviewer sees it with the draft.", media.URL(m.ID), aspect, item.ID), nil
}

func toolCreatePostDeliverable(ctx context.Context, r *crew.Run, raw json.RawMessage) (string, error) {
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
	payload := PostPayload{Content: d.Content, ThreadParts: d.ThreadParts, Hashtags: d.Hashtags, ImagePrompt: d.ImagePrompt,
		Platforms: []string{d.Platform}, MediaURLs: mediaURLs, Topic: a.Topic, LinkURL: a.Link, ScheduledAt: publishAt}
	if pt := resolvePublishTarget(ctx, r.Deps, r.Task); pt != nil {
		if pls, ok := pt.platforms(); ok && !slices.Contains(pls, d.Platform) {
			return "", fmt.Errorf("this task publishes only to %s (the accounts chosen for it); do not write a %s draft",
				strings.Join(pls, ", "), d.Platform)
		}
		id := pt.connectorID
		payload.PublisherConnectorID, payload.Accounts = &id, pt.accountsOn(d.Platform)
	}
	src, _ := json.Marshal(map[string]any{"task": r.Task.Number, "agent": r.Agent.Name, "urls": a.SourceURLs})
	payload.SourceContext = src
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	taskID := r.Task.ID
	item, err := deliverable.NewStore(r.Store).Create(ctx, deliverable.New{TaskID: &taskID, Kind: PostKind, Title: d.Title, Payload: rawPayload}, deliverable.ActorAgent, true)
	if err != nil {
		return "", err
	}
	r.Inc("drafts")
	r.Broker.Publish(item.OrgID, "deliverable.updated", map[string]any{"id": item.ID, "status": item.Status})
	msg := fmt.Sprintf("Draft %s created for %s and queued for human review (not published).", item.ID, d.Platform)
	if p, err := DecodePostPayload(item.Payload); err == nil && len(p.Warnings) > 0 {
		msg += " Warnings: " + strings.Join(p.Warnings, "; ")
	}
	return msg, nil
}

// toolListPostDeliverables lists post deliverables, newest first.
func toolListPostDeliverables(ctx context.Context, r *crew.Run, raw json.RawMessage) (string, error) {
	var a struct {
		Status string
		Limit  int
	}
	if err := crew.DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	limit := a.Limit
	if limit <= 0 || limit > 30 {
		limit = 10
	}
	type row struct {
		ID        uuid.UUID `json:"id"`
		Status    string    `json:"status"`
		Platforms []string  `json:"platforms"`
		Title     string    `json:"title"`
		Excerpt   string    `json:"excerpt"`
		Warnings  []string  `json:"warnings,omitempty"`
		updated   time.Time
	}
	status := strings.ToUpper(strings.TrimSpace(a.Status))
	out := []row{}
	df := deliverable.Filter{OrgID: &r.Task.OrgID, Kind: PostKind, Limit: limit}
	if status != "" {
		st := deliverable.Status(status)
		if status == "PUBLISHED" {
			st = deliverable.StatusDone
		}
		if !st.Valid() {
			return "", fmt.Errorf("unknown status %q", a.Status)
		}
		df.Statuses = []deliverable.Status{st}
	}
	items, _, err := deliverable.NewStore(r.Store).List(ctx, df)
	if err != nil {
		return "", err
	}
	for _, it := range items {
		p, perr := DecodePostPayload(it.Payload)
		if perr != nil {
			continue
		}
		st := string(it.Status)
		if it.Status == deliverable.StatusDone {
			st = "PUBLISHED"
		}
		out = append(out, row{it.ID, st, p.Platforms, it.Title, textutil.Clip(p.Content, 240), p.Warnings, it.UpdatedAt})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].updated.After(out[j].updated) })
	if len(out) > limit {
		out = out[:limit]
	}
	return crew.JSONResult(out)
}

func toolRevisePostDeliverable(ctx context.Context, r *crew.Run, raw json.RawMessage) (string, error) {
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
	ds := deliverable.NewStore(r.Store)
	cur, err := ds.Get(ctx, id)
	if err == nil && (cur.OrgID != r.Task.OrgID || cur.Kind != PostKind) {
		err = domain.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !cur.Status.Editable() {
		return "", fmt.Errorf("post is %s and can no longer be edited", cur.Status)
	}
	p, err := DecodePostPayload(cur.Payload)
	if err != nil {
		return "", err
	}
	if a.Content != nil {
		p.Content = *a.Content
	}
	if a.ThreadParts != nil {
		p.ThreadParts = *a.ThreadParts
	}
	if a.Hashtags != nil {
		p.Hashtags = *a.Hashtags
	}
	if a.ImagePrompt != nil {
		p.ImagePrompt = *a.ImagePrompt
	}
	if a.Topic != nil {
		p.Topic = *a.Topic
	}
	if a.Link != nil {
		p.LinkURL = *a.Link
	}
	if a.MediaURLs != nil {
		urls, err := cleanMediaURLs(*a.MediaURLs)
		if err != nil {
			return "", err
		}
		p.MediaURLs = urls
	}
	rawPayload, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	note := fmt.Sprintf("revised by @%s", r.Agent.Slug)
	if a.Note != "" {
		note += ": " + a.Note
	}
	item, err := ds.Update(ctx, id, cur.Version, deliverable.Patch{Title: a.Title, Payload: rawPayload}, deliverable.ActorAgent, note)
	if err != nil {
		return "", err
	}
	r.Broker.Publish(item.OrgID, "deliverable.updated", map[string]any{"id": item.ID, "status": item.Status})
	msg := "Draft revised."
	if np, err := DecodePostPayload(item.Payload); err == nil && len(np.Warnings) > 0 {
		msg += " Warnings: " + strings.Join(np.Warnings, "; ")
	}
	return msg, nil
}
