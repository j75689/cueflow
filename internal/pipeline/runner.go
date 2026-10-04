// Package pipeline runs a scheduled job end to end: collect material from sources, let the agent
// write drafts, store them as PENDING_REVIEW posts, and notify the reviewer.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/agent"
	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/events"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/store"
)

var ErrAlreadyRunning = errors.New("pipeline is already running")

type Runner struct {
	store     *store.Store
	hub       *hub.Hub
	broker    *events.Broker
	logger    *slog.Logger
	publicURL string
	timeout   time.Duration

	wg sync.WaitGroup
	// baseCtx is cancelled on shutdown so in-flight runs stop.
	baseCtx context.Context
}

func NewRunner(ctx context.Context, st *store.Store, h *hub.Hub, b *events.Broker, logger *slog.Logger, publicURL string) *Runner {
	return &Runner{store: st, hub: h, broker: b, logger: logger, publicURL: strings.TrimRight(publicURL, "/"),
		timeout: 30 * time.Minute, baseCtx: ctx}
}

// Trigger starts a run asynchronously and returns its ID.
func (r *Runner) Trigger(ctx context.Context, pipelineID uuid.UUID, trigger string) (uuid.UUID, error) {
	run, ok, err := r.store.StartRun(ctx, pipelineID, trigger)
	if err != nil {
		return uuid.Nil, err
	}
	if !ok {
		return uuid.Nil, ErrAlreadyRunning
	}
	r.broker.Publish("run.updated", map[string]any{"id": run.ID, "pipeline_id": pipelineID, "status": run.Status})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		runCtx, cancel := context.WithTimeout(r.baseCtx, r.timeout)
		defer cancel()
		r.execute(runCtx, pipelineID, run.ID)
	}()
	return run.ID, nil
}

// Wait blocks until in-flight runs finish (used on shutdown and in tests).
func (r *Runner) Wait() { r.wg.Wait() }

type runState struct {
	r     *Runner
	runID uuid.UUID
	pid   uuid.UUID
	stats map[string]any
}

func (s *runState) log(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.r.logger.Log(context.Background(), levelOf(level), msg, "run", s.runID)
	_ = s.r.store.AppendRunLog(context.Background(), s.runID, store.RunLogEntry{At: time.Now().UTC(), Level: level, Msg: msg})
	s.r.broker.Publish("run.updated", map[string]any{"id": s.runID, "pipeline_id": s.pid, "status": "RUNNING"})
}

func levelOf(l string) slog.Level {
	switch l {
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

func (r *Runner) execute(ctx context.Context, pipelineID, runID uuid.UUID) {
	st := &runState{r: r, runID: runID, pid: pipelineID, stats: map[string]any{
		"items_fetched": 0, "items_used": 0, "posts_created": 0, "tool_calls": 0,
	}}
	err := r.run(ctx, st)
	status, msg := "SUCCEEDED", ""
	if err != nil {
		status, msg = "FAILED", err.Error()
		st.log("error", "run failed: %v", err)
	}
	if ferr := r.store.FinishRun(context.Background(), runID, status, msg, st.stats); ferr != nil {
		r.logger.Error("finish run", "err", ferr)
	}
	r.broker.Publish("run.updated", map[string]any{"id": runID, "pipeline_id": pipelineID, "status": status})
}

func (r *Runner) run(ctx context.Context, st *runState) error {
	p, err := r.store.GetPipeline(ctx, st.pid)
	if err != nil {
		return err
	}
	llmID := p.LLMConnectorID
	if llmID == nil {
		c, err := r.store.FirstActiveConnector(ctx, connector.KindLLM)
		if err != nil {
			return fmt.Errorf("no LLM connector configured for this pipeline")
		}
		llmID = &c.ID
	}
	model, llmConn, err := r.hub.LLM(ctx, *llmID)
	if err != nil {
		return fmt.Errorf("LLM connector: %w", err)
	}
	defer hub.Close(model)
	st.log("info", "using %s (%s)", llmConn.Name, model.Model())

	// 1. Ingest (delta fetch) from each source.
	since := time.Now().Add(-time.Duration(p.LookbackHours) * time.Hour)
	fetched := 0
	for _, sid := range p.SourceConnectorIDs {
		n, err := r.hub.FetchSource(ctx, sid, since, 0)
		if err != nil {
			st.log("warn", "source %s: %v", sid, err)
			continue
		}
		fetched += n
		st.log("info", "source %s: %d new items", r.connectorName(ctx, sid), n)
	}
	st.stats["items_fetched"] = fetched

	items, err := r.store.UnconsumedItems(ctx, p.SourceConnectorIDs, since, p.MaxItems)
	if err != nil {
		return err
	}
	st.stats["items_used"] = len(items)

	var searcher connector.Searcher
	if p.SearchConnectorID != nil {
		s, c, err := r.hub.Searcher(ctx, *p.SearchConnectorID)
		if err != nil {
			st.log("warn", "search connector unavailable: %v", err)
		} else {
			searcher = s
			defer hub.Close(s)
			st.log("info", "web search enabled via %s", c.Name)
		}
	}

	if len(items) == 0 && searcher == nil {
		st.log("info", "no new material in the last %dh and no search engine configured; nothing to write", p.LookbackHours)
		return nil
	}
	st.log("info", "generating drafts from %d items for %s", len(items), strings.Join(p.Platforms, ", "))

	// 2. Generate.
	names := map[uuid.UUID]string{}
	materials := make([]agent.Material, 0, len(items))
	byRef := map[string]*store.SourceItem{}
	for i, it := range items {
		if _, ok := names[it.ConnectorID]; !ok {
			names[it.ConnectorID] = r.connectorName(ctx, it.ConnectorID)
		}
		ref := fmt.Sprintf("S%d", i+1)
		byRef[ref] = it
		materials = append(materials, agent.Material{Ref: ref, Source: names[it.ConnectorID], Title: it.Title,
			URL: it.URL, Author: it.Author, Content: it.Content, PublishedAt: it.PublishedAt})
	}
	gen := &agent.Generator{LLM: model, Search: searcher, MaxContentChar: 1500,
		Logf: func(level, msg string) { st.log(level, "%s", msg) }}
	res, err := gen.Run(ctx, agent.Input{
		Topic: p.Topic, Instructions: p.Instructions, Language: p.Language, Platforms: p.Platforms,
		Materials: materials, SearchQueries: p.SearchQueries,
	})
	if res != nil {
		st.stats["tool_calls"] = res.ToolCalls
		st.stats["input_tokens"] = res.Usage.InputTokens
		st.stats["output_tokens"] = res.Usage.OutputTokens
	}
	if err != nil {
		return err
	}

	// 3. Store drafts as PENDING_REVIEW.
	groupID := uuid.New()
	var created []*domain.Post
	for _, d := range res.Drafts {
		post, err := r.store.CreatePost(ctx, store.NewPost{
			GroupID: &groupID, PipelineID: &p.ID, RunID: &st.runID, PublisherConnectorID: p.PublisherConnectorID,
			Title: d.Title, Content: d.Content, ThreadParts: d.ThreadParts, Hashtags: d.Hashtags,
			ImagePrompt: d.ImagePrompt, Platforms: []string{d.Platform},
			SourceContext: sourceContext(p, d, byRef, res.SearchResults),
		}, domain.ActorAgent, true)
		if err != nil {
			return fmt.Errorf("store draft: %w", err)
		}
		created = append(created, post)
		r.broker.Publish("post.updated", map[string]any{"id": post.ID, "status": post.Status})
	}
	st.stats["posts_created"] = len(created)
	st.log("info", "created %d drafts awaiting review", len(created))

	ids := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	if err := r.store.MarkItemsConsumed(ctx, ids); err != nil {
		st.log("warn", "mark items consumed: %v", err)
	}

	// 4. Notify.
	if p.NotifierConnectorID != nil && len(created) > 0 {
		if err := r.notify(ctx, *p.NotifierConnectorID, p.Name, created); err != nil {
			st.log("warn", "notification failed: %v", err)
		} else {
			st.log("info", "review reminder sent")
		}
	}
	return nil
}

func (r *Runner) connectorName(ctx context.Context, id uuid.UUID) string {
	if c, err := r.store.GetConnector(ctx, id); err == nil {
		return c.Name
	}
	return id.String()
}

func sourceContext(p *store.Pipeline, d agent.Draft, byRef map[string]*store.SourceItem, search []connector.SearchResult) map[string]any {
	type ref struct {
		Ref   string `json:"ref"`
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	refs := []ref{}
	for _, r := range d.SourceRefs {
		if it, ok := byRef[strings.TrimSpace(r)]; ok {
			refs = append(refs, ref{Ref: r, Title: it.Title, URL: it.URL})
		}
	}
	searchRefs := []map[string]string{}
	for i, s := range search {
		if i >= 10 {
			break
		}
		searchRefs = append(searchRefs, map[string]string{"title": s.Title, "url": s.URL})
	}
	return map[string]any{"topic": p.Topic, "pipeline": p.Name, "items": refs, "search": searchRefs}
}

func (r *Runner) notify(ctx context.Context, notifierID uuid.UUID, pipelineName string, posts []*domain.Post) error {
	n, _, err := r.hub.Notifier(ctx, notifierID)
	if err != nil {
		return err
	}
	defer hub.Close(n)
	return n.Notify(ctx, ReviewDigest(r.publicURL, fmt.Sprintf("[CueFlow] %d new drafts from %q awaiting review", len(posts), pipelineName), posts))
}

// ReviewDigest renders a review reminder listing posts with links into the dashboard.
func ReviewDigest(publicURL, subject string, posts []*domain.Post) connector.Notification {
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
