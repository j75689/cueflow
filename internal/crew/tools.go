package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/agent"
	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/media"
	"github.com/j75689/cueflow/internal/store"
)

// Capability groups tools an agent may use. No capability can publish: drafts always go to the
// human review queue, and publishers are only reachable from the dispatch worker.
type Capability struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tools       []string `json:"tools"`
}

type tool struct {
	name        string
	description string
	schema      map[string]any
	capability  string // "" = every agent
	run         func(ctx context.Context, e *runEnv, args json.RawMessage) (string, error)
}

// outcome is set by a terminal tool and ends the run.
type outcome struct {
	kind string // complete | ask | wait | limit | error
	text string
}

type runEnv struct {
	c        *Crew
	task     *store.Task
	agent    *store.Agent
	outcome  *outcome
	subtasks int
	drafts   int
	images   int

	searcher    connector.Searcher
	searcherErr error
	searchTried bool
}

const (
	maxSubtasksPerRun = 8
	maxDraftsPerRun   = 10
	maxImagesPerRun   = 2
	maxDelegateDepth  = 3
	maxToolResult     = 12000
)

func obj(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func strList(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}
func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

var priorityEnum = map[string]any{"type": "string", "enum": []string{"low", "normal", "high", "urgent"}}

var capabilities = []Capability{
	{ID: "research", Name: "Research", Description: "Search the web, read pages, and query material collected by source connectors."},
	{ID: "drafting", Name: "Drafting", Description: "Write social post drafts into the human review queue and revise drafts that are still pending."},
	{ID: "imaging", Name: "Images", Description: "Generate images for drafts with the image connector (each image costs money; at most a few per run)."},
	{ID: "delegation", Name: "Delegation", Description: "Split work into subtasks for teammates and wait for their results."},
	{ID: "planning", Name: "Planning", Description: "Look at and create tasks on the board, and set up recurring schedules."},
	{ID: "memory", Name: "Memory", Description: "Remember facts and preferences across tasks."},
}

var tools []tool

func init() {
	tools = []tool{
		// --- always available ---
		{name: "add_comment", description: "Post a short progress update to the task thread (visible to the human).",
			schema: obj(map[string]any{"content": str("Update text")}, "content"), run: toolAddComment},
		{name: "ask_human", description: "Ask the human a question and pause until they reply. Use only when you genuinely need their decision or missing information.",
			schema: obj(map[string]any{"question": str("The question, with options if helpful")}, "question"), run: toolAskHuman},
		{name: "complete_task", description: "Finish the task with a concise summary of what you did and the outputs (draft IDs, subtask numbers, links).",
			schema: obj(map[string]any{"summary": str("Result summary for the human")}, "summary"), run: toolComplete},

		// --- research ---
		{name: "web_search", capability: "research", description: "Search the web for recent information.",
			schema: obj(map[string]any{"query": str("Search query"), "limit": integer("Max results (default 5)")}, "query"), run: toolWebSearch},
		{name: "read_url", capability: "research", description: "Read the text of a public web page.",
			schema: obj(map[string]any{"url": str("Absolute http(s) URL")}, "url"), run: toolReadURL},
		{name: "search_materials", capability: "research", description: "Search items already collected from RSS feeds and mailboxes.",
			schema: obj(map[string]any{"query": str("Keyword (blank = latest)"), "days": integer("Look back this many days (default 7)"), "limit": integer("Max items (default 10)")}), run: toolSearchMaterials},
		{name: "fetch_sources", capability: "research", description: "Pull the latest items from your source connectors (RSS, IMAP) right now.",
			schema: obj(map[string]any{}), run: toolFetchSources},

		// --- drafting ---
		{name: "create_post_draft", capability: "drafting", description: "Create one social post draft for one platform. It goes to the human review queue; it is NOT published.",
			schema: obj(map[string]any{
				"platform":     map[string]any{"type": "string", "enum": platformIDs()},
				"title":        str("Short internal title for the reviewer"),
				"content":      str("Post body, plain text, no hashtags"),
				"thread_parts": strList("Ordered thread parts for thread-capable platforms (optional)"),
				"hashtags":     strList("Hashtags without '#'"),
				"image_prompt": str("Prompt for an accompanying image (Instagram)"),
				"topic":        str("Threads only: one topic tag, up to 50 characters, no '.' or '&' (optional)"),
				"link":         str("Optional URL shown as a link preview card (Threads), e.g. the main source"),
				"source_urls":  strList("URLs backing the claims in this post"),
			}, "platform", "content"), run: toolCreateDraft},
		{name: "list_posts", capability: "drafting", description: "List recent posts and drafts with their review status.",
			schema: obj(map[string]any{"status": str("Optional status filter, e.g. PENDING_REVIEW"), "limit": integer("Max posts (default 10)")}), run: toolListPosts},
		{name: "revise_draft", capability: "drafting", description: "Revise a draft that is still DRAFT or PENDING_REVIEW.",
			schema: obj(map[string]any{
				"post_id": str("Post ID"), "title": str("New title"), "content": str("New body"),
				"thread_parts": strList("New thread parts"), "hashtags": strList("New hashtags"),
				"image_prompt": str("New image prompt"), "topic": str("New Threads topic tag (empty removes it)"),
				"link": str("New link card URL (empty removes it)"), "note": str("Why you changed it"),
			}, "post_id"), run: toolReviseDraft},

		// --- imaging ---
		{name: "generate_image", capability: "imaging", description: fmt.Sprintf("Generate an image for a draft that is still DRAFT or PENDING_REVIEW and attach it. Uses the draft's image_prompt unless you give a prompt. Costs money: at most %d per run.", maxImagesPerRun),
			schema: obj(map[string]any{
				"post_id": str("Draft (post) ID"),
				"prompt":  str("Image prompt in English: subject, style, composition, no text in the image (optional; defaults to the draft's image_prompt)"),
				"aspect":  map[string]any{"type": "string", "enum": connector.Aspects, "description": "Aspect ratio (default 4:5 for Instagram, else 1:1)"},
			}, "post_id"), run: toolGenerateImage},

		// --- delegation ---
		{name: "list_agents", capability: "delegation", description: "List teammates you can delegate to.",
			schema: obj(map[string]any{}), run: toolListAgents},
		{name: "create_subtask", capability: "delegation", description: "Delegate part of this task to a teammate. They start immediately.",
			schema: obj(map[string]any{
				"title": str("Clear, specific title"), "description": str("Everything they need: goal, context, expected output"),
				"assignee": str("Teammate slug, e.g. writer"), "priority": priorityEnum,
			}, "title", "description", "assignee"), run: toolCreateSubtask},
		{name: "wait_for_subtasks", capability: "delegation", description: "Pause until all your subtasks finish; you will be resumed with their results.",
			schema: obj(map[string]any{}), run: toolWaitSubtasks},

		// --- planning ---
		{name: "list_tasks", capability: "planning", description: "List tasks on the board.",
			schema: obj(map[string]any{"status": str("Optional status filter"), "assignee": str("Optional agent slug"), "limit": integer("Max tasks (default 20)")}), run: toolListTasks},
		{name: "get_task", capability: "planning", description: "Read a task by number, including its result and subtasks.",
			schema: obj(map[string]any{"number": integer("Task number")}, "number"), run: toolGetTask},
		{name: "create_task", capability: "planning", description: "Create a separate task on the board (not a subtask). Assign it to start it.",
			schema: obj(map[string]any{
				"title": str("Title"), "description": str("Details"), "assignee": str("Optional agent slug"),
				"priority": priorityEnum,
			}, "title"), run: toolCreateTask},
		{name: "schedule_task", capability: "planning", description: "Create a recurring schedule that opens a task for an agent on a cron expression (minute hour day month weekday), at most every 5 minutes.",
			schema: obj(map[string]any{
				"name": str("Schedule name"), "cron": str("Cron, e.g. '0 8 * * *' for 08:00 daily"),
				"timezone": str("IANA timezone, e.g. Asia/Taipei"), "title": str("Title of each task"),
				"description": str("Instructions for each run"), "assignee": str("Agent slug (default: yourself)"),
				"priority": priorityEnum,
			}, "cron", "title", "description"), run: toolScheduleTask},
		{name: "list_schedules", capability: "planning", description: "List recurring schedules.",
			schema: obj(map[string]any{}), run: toolListSchedules},
		{name: "pause_schedule", capability: "planning", description: "Pause a recurring schedule by ID.",
			schema: obj(map[string]any{"id": str("Schedule ID")}, "id"), run: toolPauseSchedule},

		// --- memory ---
		{name: "remember", capability: "memory", description: "Save a durable note (a preference, fact, or decision) for future tasks.",
			schema: obj(map[string]any{"content": str("The note")}, "content"), run: toolRemember},
		{name: "search_memory", capability: "memory", description: "Search your saved notes.",
			schema: obj(map[string]any{"query": str("Keyword")}, "query"), run: toolSearchMemory},
	}
	byCap := map[string][]string{}
	for _, t := range tools {
		if t.capability != "" {
			byCap[t.capability] = append(byCap[t.capability], t.name)
		}
	}
	for i := range capabilities {
		capabilities[i].Tools = byCap[capabilities[i].ID]
	}
	store.ValidCapability = func(c string) bool {
		_, ok := byCap[c]
		return ok
	}
}

// Capabilities returns the capability catalog for the UI.
func Capabilities() []Capability { return capabilities }

func platformIDs() []string {
	var ids []string
	for _, p := range domain.Platforms() {
		ids = append(ids, p.ID)
	}
	return ids
}

func toolsFor(a *store.Agent) []tool {
	var out []tool
	for _, t := range tools {
		if t.capability == "" || a.Has(t.capability) {
			out = append(out, t)
		}
	}
	return out
}

func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

func jsonResult(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// --- base tools ---

func toolAddComment(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ Content string }
	if err := decodeArgs(raw, &a); err != nil || strings.TrimSpace(a.Content) == "" {
		return "", errors.New("content is required")
	}
	e.c.say(ctx, e.task.ID, e.agent, "message", a.Content, nil)
	return "posted", nil
}

func toolAskHuman(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ Question string }
	if err := decodeArgs(raw, &a); err != nil || strings.TrimSpace(a.Question) == "" {
		return "", errors.New("question is required")
	}
	e.outcome = &outcome{kind: "ask", text: a.Question}
	return "Question posted. Stop now; you will be resumed when the human replies.", nil
}

func toolComplete(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ Summary string }
	if err := decodeArgs(raw, &a); err != nil || strings.TrimSpace(a.Summary) == "" {
		return "", errors.New("summary is required")
	}
	e.outcome = &outcome{kind: "complete", text: a.Summary}
	return "Task completed.", nil
}

// --- research ---

func (e *runEnv) search(ctx context.Context) (connector.Searcher, error) {
	if e.searchTried {
		return e.searcher, e.searcherErr
	}
	e.searchTried = true
	id := e.agent.SearchConnectorID
	if id == nil {
		c, err := e.c.store.FirstActiveConnector(ctx, connector.KindSearch)
		if err != nil {
			e.searcherErr = errors.New("no search connector configured (add SearXNG or Tavily under Connectors)")
			return nil, e.searcherErr
		}
		id = &c.ID
	}
	e.searcher, _, e.searcherErr = e.c.hub.Searcher(ctx, *id)
	return e.searcher, e.searcherErr
}

func toolWebSearch(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Query string
		Limit int
	}
	if err := decodeArgs(raw, &a); err != nil || strings.TrimSpace(a.Query) == "" {
		return "", errors.New("query is required")
	}
	if a.Limit <= 0 || a.Limit > 10 {
		a.Limit = 5
	}
	s, err := e.search(ctx)
	if err != nil {
		return "", err
	}
	res, err := s.Search(ctx, a.Query, a.Limit)
	if err != nil {
		return "", err
	}
	return jsonResult(res)
}

func toolReadURL(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ URL string }
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	page, err := e.c.fetcher.Fetch(ctx, a.URL)
	if err != nil {
		return "", err
	}
	return jsonResult(page)
}

func toolSearchMaterials(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Query string
		Days  int
		Limit int
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if a.Days <= 0 || a.Days > 90 {
		a.Days = 7
	}
	items, err := e.c.store.SearchItems(ctx, e.agent.SourceConnectorIDs, a.Query, time.Now().AddDate(0, 0, -a.Days), a.Limit)
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
		r := row{Title: it.Title, URL: it.URL, Author: it.Author, Excerpt: clip(it.Content, 600)}
		if it.PublishedAt != nil {
			r.Published = it.PublishedAt.Format("2006-01-02")
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return "No matching material. Try fetch_sources first, or web_search.", nil
	}
	return jsonResult(out)
}

func toolFetchSources(ctx context.Context, e *runEnv, _ json.RawMessage) (string, error) {
	ids := e.agent.SourceConnectorIDs
	if len(ids) == 0 {
		cs, err := e.c.store.ListConnectors(ctx, string(connector.KindSource))
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
		n, err := e.c.hub.FetchSource(ctx, id, time.Now().AddDate(0, 0, -7), 0)
		name := id.String()
		if c, cerr := e.c.store.GetConnector(ctx, id); cerr == nil {
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

func toolCreateDraft(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Platform    string   `json:"platform"`
		Title       string   `json:"title"`
		Content     string   `json:"content"`
		ThreadParts []string `json:"thread_parts"`
		Hashtags    []string `json:"hashtags"`
		ImagePrompt string   `json:"image_prompt"`
		Topic       string   `json:"topic"`
		Link        string   `json:"link"`
		SourceURLs  []string `json:"source_urls"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if e.drafts >= maxDraftsPerRun {
		return "", fmt.Errorf("draft limit reached (%d per run)", maxDraftsPerRun)
	}
	if _, ok := domain.PlatformByID(a.Platform); !ok {
		return "", fmt.Errorf("unknown platform %q (use one of %s)", a.Platform, strings.Join(platformIDs(), ", "))
	}
	drafts := agent.Normalize([]agent.Draft{{Platform: a.Platform, Title: a.Title, Content: a.Content,
		ThreadParts: a.ThreadParts, Hashtags: a.Hashtags, ImagePrompt: a.ImagePrompt}}, []string{a.Platform})
	if len(drafts) == 0 {
		return "", errors.New("content is empty")
	}
	d := drafts[0]
	taskID := e.task.ID
	publisher, accounts := e.c.publishDefaults(ctx, e.task, d.Platform)
	post, err := e.c.store.CreatePost(ctx, store.NewPost{
		TaskID: &taskID, Title: d.Title, Content: d.Content, ThreadParts: d.ThreadParts, Hashtags: d.Hashtags,
		ImagePrompt: d.ImagePrompt, Platforms: []string{d.Platform}, Topic: a.Topic, LinkURL: a.Link,
		PublisherConnectorID: publisher, Accounts: accounts,
		SourceContext: map[string]any{"task": e.task.Number, "agent": e.agent.Name, "urls": a.SourceURLs},
	}, domain.ActorAgent, true)
	if err != nil {
		return "", err
	}
	e.drafts++
	e.c.broker.Publish("post.updated", map[string]any{"id": post.ID, "status": post.Status})
	msg := fmt.Sprintf("Draft %s created for %s and queued for human review (not published).", post.ID, d.Platform)
	if len(post.Warnings) > 0 {
		msg += " Warnings: " + strings.Join(post.Warnings, "; ")
	}
	return msg, nil
}

// publishDefaults returns the task's publisher and the default accounts that are on platform.
// Accounts are filtered through the publisher's account list; if it cannot be read, all are kept
// and the reviewer can fix them.
func (c *Crew) publishDefaults(ctx context.Context, t *store.Task, platform string) (*uuid.UUID, []string) {
	if t.PublisherConnectorID == nil {
		return nil, nil
	}
	if len(t.PublishAccounts) == 0 {
		return t.PublisherConnectorID, nil
	}
	inst, _, err := c.hub.Instance(ctx, *t.PublisherConnectorID, true)
	if err != nil {
		return t.PublisherConnectorID, t.PublishAccounts
	}
	defer hub.Close(inst)
	al, ok := inst.(connector.AccountLister)
	if !ok {
		return t.PublisherConnectorID, nil
	}
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	list, err := al.ListAccounts(lctx)
	if err != nil {
		c.logger.Warn("list publisher accounts", "err", err)
		return t.PublisherConnectorID, t.PublishAccounts
	}
	on := map[string]string{}
	for _, a := range list {
		on[a.ID] = a.Platform
	}
	var out []string
	for _, id := range t.PublishAccounts {
		if on[id] == platform {
			out = append(out, id)
		}
	}
	return t.PublisherConnectorID, out
}

func toolListPosts(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Status string
		Limit  int
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	f := store.PostFilter{Limit: a.Limit}
	if f.Limit <= 0 || f.Limit > 30 {
		f.Limit = 10
	}
	if st := domain.Status(strings.ToUpper(strings.TrimSpace(a.Status))); st != "" {
		if !st.Valid() {
			return "", fmt.Errorf("unknown status %q", a.Status)
		}
		f.Statuses = []domain.Status{st}
	}
	posts, _, err := e.c.store.ListPosts(ctx, f)
	if err != nil {
		return "", err
	}
	type row struct {
		ID        uuid.UUID     `json:"id"`
		Status    domain.Status `json:"status"`
		Platforms []string      `json:"platforms"`
		Title     string        `json:"title"`
		Excerpt   string        `json:"excerpt"`
		Warnings  []string      `json:"warnings,omitempty"`
	}
	out := []row{}
	for _, p := range posts {
		out = append(out, row{p.ID, p.Status, p.Platforms, p.Title, clip(p.Content, 240), p.Warnings})
	}
	return jsonResult(out)
}

func toolReviseDraft(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		PostID      string    `json:"post_id"`
		Title       *string   `json:"title"`
		Content     *string   `json:"content"`
		ThreadParts *[]string `json:"thread_parts"`
		Hashtags    *[]string `json:"hashtags"`
		ImagePrompt *string   `json:"image_prompt"`
		Topic       *string   `json:"topic"`
		Link        *string   `json:"link"`
		Note        string    `json:"note"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	id, err := uuid.Parse(strings.TrimSpace(a.PostID))
	if err != nil {
		return "", errors.New("post_id must be a post UUID")
	}
	cur, err := e.c.store.GetPost(ctx, id)
	if err != nil {
		return "", err
	}
	if !cur.Status.Editable() {
		return "", fmt.Errorf("post is %s and can no longer be edited", cur.Status)
	}
	note := fmt.Sprintf("revised by @%s", e.agent.Slug)
	if a.Note != "" {
		note += ": " + a.Note
	}
	p, err := e.c.store.UpdatePostAs(ctx, id, cur.Version, store.PostPatch{Title: a.Title, Content: a.Content,
		ThreadParts: a.ThreadParts, Hashtags: a.Hashtags, ImagePrompt: a.ImagePrompt, Topic: a.Topic, LinkURL: a.Link},
		domain.ActorAgent, note)
	if err != nil {
		return "", err
	}
	e.c.broker.Publish("post.updated", map[string]any{"id": p.ID, "status": p.Status})
	msg := "Draft revised."
	if len(p.Warnings) > 0 {
		msg += " Warnings: " + strings.Join(p.Warnings, "; ")
	}
	return msg, nil
}

// --- imaging ---

func toolGenerateImage(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		PostID string `json:"post_id"`
		Prompt string `json:"prompt"`
		Aspect string `json:"aspect"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if e.images >= maxImagesPerRun {
		return "", fmt.Errorf("image limit reached (%d per run)", maxImagesPerRun)
	}
	id, err := uuid.Parse(strings.TrimSpace(a.PostID))
	if err != nil {
		return "", errors.New("post_id must be a post UUID")
	}
	post, err := e.c.store.GetPost(ctx, id)
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
	e.images++ // count attempts too: a failed generation may still be billed
	m, err := (&media.Service{Store: e.c.store, Hub: e.c.hub}).Generate(ctx, media.GenerateRequest{
		Prompt: prompt, Aspect: aspect, CreatedBy: e.agent.Slug})
	if err != nil {
		return "", err
	}
	urls := append(append([]string{}, post.MediaURLs...), media.URL(m.ID))
	p, err := e.c.store.UpdatePostAs(ctx, id, post.Version, store.PostPatch{MediaURLs: &urls}, domain.ActorAgent,
		fmt.Sprintf("image generated by @%s", e.agent.Slug))
	if err != nil {
		return "", err
	}
	e.c.broker.Publish("post.updated", map[string]any{"id": p.ID, "status": p.Status})
	return fmt.Sprintf("Image %s generated (%s) and attached to draft %s. The reviewer sees it with the draft.", media.URL(m.ID), aspect, p.ID), nil
}

// --- delegation ---

func toolListAgents(ctx context.Context, e *runEnv, _ json.RawMessage) (string, error) {
	agents, err := e.c.store.ListAgents(ctx)
	if err != nil {
		return "", err
	}
	type row struct {
		Slug         string   `json:"slug"`
		Name         string   `json:"name"`
		Role         string   `json:"role"`
		Description  string   `json:"description"`
		Capabilities []string `json:"capabilities"`
		OpenTasks    int      `json:"open_tasks"`
	}
	out := []row{}
	for _, a := range agents {
		if a.IsActive {
			out = append(out, row{a.Slug, a.Name, a.Role, a.Description, a.Capabilities, a.Stats.OpenTasks})
		}
	}
	return jsonResult(out)
}

func (e *runEnv) resolveAgent(ctx context.Context, slug string) (*store.Agent, error) {
	if strings.TrimSpace(slug) == "" {
		return e.agent, nil
	}
	a, err := e.c.store.GetAgentBySlug(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("no teammate with slug %q (use list_agents)", slug)
	}
	if !a.IsActive {
		return nil, fmt.Errorf("@%s is disabled", a.Slug)
	}
	return a, nil
}

func toolCreateSubtask(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Title, Description, Assignee, Priority string
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if e.subtasks >= maxSubtasksPerRun {
		return "", fmt.Errorf("subtask limit reached (%d per run)", maxSubtasksPerRun)
	}
	depth, err := e.c.store.TaskDepth(ctx, e.task.ID)
	if err != nil {
		return "", err
	}
	if depth+1 >= maxDelegateDepth {
		return "", errors.New("delegation is nested too deeply; do this part yourself")
	}
	assignee, err := e.resolveAgent(ctx, a.Assignee)
	if err != nil {
		return "", err
	}
	parent := e.task.ID
	creator := e.agent.ID
	t, err := e.c.store.CreateTask(ctx, store.NewTask{
		Title: a.Title, Description: a.Description, Priority: a.Priority, Status: store.TaskTodo,
		AssigneeAgentID: &assignee.ID, CreatedBy: "agent", CreatorAgentID: &creator, ParentID: &parent,
		PublisherConnectorID: e.task.PublisherConnectorID, PublishAccounts: e.task.PublishAccounts,
	})
	if err != nil {
		return "", err
	}
	e.subtasks++
	e.c.system(ctx, t.ID, fmt.Sprintf("Delegated by @%s from task #%d.", e.agent.Slug, e.task.Number))
	e.c.broker.Publish("task.updated", map[string]any{"id": t.ID, "status": t.Status})
	e.c.Wake()
	return fmt.Sprintf("Subtask #%d created and assigned to @%s. Call wait_for_subtasks when you have delegated everything.", t.Number, assignee.Slug), nil
}

func toolWaitSubtasks(ctx context.Context, e *runEnv, _ json.RawMessage) (string, error) {
	n, err := e.c.store.OpenSubtasks(ctx, e.task.ID)
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", errors.New("there are no unfinished subtasks; continue the work or complete the task")
	}
	e.outcome = &outcome{kind: "wait", text: fmt.Sprintf("Waiting for %d subtask(s).", n)}
	return "Paused. You will be resumed with the subtask results.", nil
}

// --- planning ---

func taskLine(t *store.Task) string {
	who := "unassigned"
	if t.Assignee != nil {
		who = "@" + t.Assignee.Slug
	}
	return fmt.Sprintf("#%d [%s] %s (%s, %s)", t.Number, t.Status, t.Title, who, t.Priority)
}

func toolListTasks(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Status, Assignee string
		Limit            int
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	f := store.TaskFilter{Limit: a.Limit}
	if f.Limit <= 0 || f.Limit > 50 {
		f.Limit = 20
	}
	if st := store.TaskStatus(strings.ToUpper(strings.TrimSpace(a.Status))); st != "" {
		if !st.Valid() {
			return "", fmt.Errorf("unknown status %q", a.Status)
		}
		f.Statuses = []store.TaskStatus{st}
	}
	if a.Assignee != "" {
		ag, err := e.resolveAgent(ctx, a.Assignee)
		if err != nil {
			return "", err
		}
		f.AssigneeID = &ag.ID
	}
	ts, err := e.c.store.ListTasks(ctx, f)
	if err != nil {
		return "", err
	}
	if len(ts) == 0 {
		return "No tasks.", nil
	}
	lines := make([]string, 0, len(ts))
	for _, t := range ts {
		lines = append(lines, taskLine(t))
	}
	return strings.Join(lines, "\n"), nil
}

func toolGetTask(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ Number int64 }
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	t, err := e.c.store.GetTaskByNumber(ctx, a.Number)
	if err != nil {
		return "", fmt.Errorf("task #%d not found", a.Number)
	}
	var b strings.Builder
	b.WriteString(taskLine(t) + "\n")
	if t.Description != "" {
		b.WriteString("Description: " + clip(t.Description, 1500) + "\n")
	}
	if t.Result != "" {
		b.WriteString("Result: " + clip(t.Result, 2000) + "\n")
	}
	subs, _ := e.c.store.ListTasks(ctx, store.TaskFilter{ParentID: &t.ID, All: true})
	for _, s := range subs {
		b.WriteString("  subtask " + taskLine(s))
		if s.Result != "" {
			b.WriteString(" — " + clip(s.Result, 400))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func toolCreateTask(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Title, Description, Assignee, Priority string
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if e.subtasks >= maxSubtasksPerRun {
		return "", fmt.Errorf("task creation limit reached (%d per run)", maxSubtasksPerRun)
	}
	in := store.NewTask{Title: a.Title, Description: a.Description, Priority: a.Priority, CreatedBy: "agent", CreatorAgentID: &e.agent.ID}
	if a.Assignee != "" {
		ag, err := e.resolveAgent(ctx, a.Assignee)
		if err != nil {
			return "", err
		}
		in.AssigneeAgentID = &ag.ID
	}
	t, err := e.c.store.CreateTask(ctx, in)
	if err != nil {
		return "", err
	}
	e.subtasks++
	e.c.system(ctx, t.ID, fmt.Sprintf("Created by @%s while working on task #%d.", e.agent.Slug, e.task.Number))
	e.c.broker.Publish("task.updated", map[string]any{"id": t.ID, "status": t.Status})
	e.c.Wake()
	return fmt.Sprintf("Task #%d created (%s).", t.Number, t.Status), nil
}

func toolScheduleTask(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Name, Cron, Timezone, Title, Description, Assignee, Priority string
	}
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	assignee, err := e.resolveAgent(ctx, a.Assignee)
	if err != nil {
		return "", err
	}
	creator := e.agent.ID
	sc, err := e.c.store.CreateSchedule(ctx, &store.Schedule{
		Name: a.Name, CronExpr: a.Cron, Timezone: a.Timezone, IsActive: true, AssigneeAgentID: &assignee.ID,
		TaskTitle: a.Title, TaskDescription: a.Description, Priority: a.Priority, CreatedBy: "agent", CreatorAgentID: &creator,
	}, e.c.ValidateSchedule)
	if err != nil {
		return "", err
	}
	e.c.schedulesChanged(ctx)
	e.c.broker.Publish("schedule.updated", map[string]any{"id": sc.ID})
	return fmt.Sprintf("Schedule %s created: %q runs at %q (%s), assigned to @%s. The human can pause it under Schedules.",
		sc.ID, sc.Name, sc.CronExpr, sc.Timezone, assignee.Slug), nil
}

func toolListSchedules(ctx context.Context, e *runEnv, _ json.RawMessage) (string, error) {
	scs, err := e.c.store.ListSchedules(ctx)
	if err != nil {
		return "", err
	}
	if len(scs) == 0 {
		return "No schedules.", nil
	}
	var lines []string
	for _, s := range scs {
		state := "active"
		if !s.IsActive {
			state = "paused"
		}
		lines = append(lines, fmt.Sprintf("%s %q %s (%s) %s — task: %s", s.ID, s.Name, s.CronExpr, s.Timezone, state, s.TaskTitle))
	}
	return strings.Join(lines, "\n"), nil
}

func toolPauseSchedule(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ ID string }
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	id, err := uuid.Parse(strings.TrimSpace(a.ID))
	if err != nil {
		return "", errors.New("id must be a schedule UUID")
	}
	sc, err := e.c.store.GetSchedule(ctx, id)
	if err != nil {
		return "", err
	}
	sc.IsActive = false
	if _, err := e.c.store.UpdateSchedule(ctx, sc, nil); err != nil {
		return "", err
	}
	e.c.schedulesChanged(ctx)
	e.c.broker.Publish("schedule.updated", map[string]any{"id": sc.ID})
	return "Schedule paused.", nil
}

// --- memory ---

func toolRemember(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ Content string }
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	if _, err := e.c.store.AddMemory(ctx, e.agent.ID, a.Content); err != nil {
		return "", err
	}
	e.c.broker.Publish("agent.updated", map[string]any{"id": e.agent.ID})
	return "Saved.", nil
}

func toolSearchMemory(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ Query string }
	if err := decodeArgs(raw, &a); err != nil {
		return "", err
	}
	ms, err := e.c.store.Memories(ctx, e.agent.ID, a.Query, 20)
	if err != nil {
		return "", err
	}
	if len(ms) == 0 {
		return "No matching notes.", nil
	}
	var lines []string
	for _, m := range ms {
		lines = append(lines, fmt.Sprintf("- (%s) %s", m.CreatedAt.Format("2006-01-02"), m.Content))
	}
	return strings.Join(lines, "\n"), nil
}
