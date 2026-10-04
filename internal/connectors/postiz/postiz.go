// Package postiz is the publisher connector that drives a self-hosted Postiz through its MCP
// server. Postiz owns platform OAuth, token refresh and the per-platform publish flows
// (e.g. Meta's two-step media containers); CueFlow only calls its MCP tools.
//
// Postiz MCP (Streamable HTTP, stateless): POST {backend}/mcp with "Authorization: Bearer <API key>".
// The key is under Settings -> Developers in the Postiz UI. Tools used:
//   - integrationList {}                         -> connected channels (id, platform, disabled)
//   - uploadFromUrlTool {url}                    -> {id, path} on the Postiz upload domain
//   - integrationSchedulePostTool {socialPost[]} -> {output:[{postId, integration}]} or {output:{errors}}
package postiz

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/mcpx"
	"github.com/j75689/cueflow/internal/textutil"
)

func init() {
	connector.Register(connector.Spec{
		Type:        "postiz_mcp",
		Kind:        connector.KindPublisher,
		Name:        "Postiz (MCP)",
		Description: "Publishes approved posts to Threads, Instagram, X, LinkedIn... through a self-hosted Postiz MCP server.",
		Fields: []connector.Field{
			{Key: "endpoint_url", Label: "MCP endpoint", Type: connector.FieldString, Required: true,
				Placeholder: "http://postiz:5000/api/mcp", Help: "Postiz backend URL + /mcp (e.g. http://localhost:4007/api/mcp)"},
			{Key: "api_key", Label: "Postiz API key", Type: connector.FieldPassword, Secret: true,
				Help: "Postiz → Settings → Developers. Sent as a Bearer token."},
			{Key: "transport", Label: "Transport", Type: connector.FieldSelect,
				Options: []string{mcpx.TransportStreamable, mcpx.TransportSSE, mcpx.TransportStdio}, Default: mcpx.TransportStreamable},
			{Key: "command", Label: "Command (stdio only)", Type: connector.FieldString},
			{Key: "args", Label: "Arguments (stdio only)", Type: connector.FieldStringList},
			{Key: "integrations", Label: "Channel mapping", Type: connector.FieldKV,
				Help: "Optional platform → Postiz integration ID (e.g. threads → clx...). Unmapped platforms use the first connected channel of that platform."},
			{Key: "post_mode", Label: "Mode", Type: connector.FieldSelect, Options: []string{"publish", "draft"}, Default: "publish",
				Help: "draft = create the post as a Postiz draft instead of publishing"},
			{Key: "upload_media", Label: "Re-upload media to Postiz", Type: connector.FieldBool, Default: true,
				Help: "Postiz only attaches files from its own upload storage"},
			{Key: "instagram_post_type", Label: "Instagram post type", Type: connector.FieldSelect, Options: []string{"post", "story"}, Default: "post"},
			{Key: "shorten_links", Label: "Shorten links", Type: connector.FieldBool, Default: false},
			{Key: "schedule_tool", Label: "Schedule tool name", Type: connector.FieldString, Default: "integrationSchedulePostTool"},
			{Key: "list_tool", Label: "Channel list tool name", Type: connector.FieldString, Default: "integrationList"},
			{Key: "upload_tool", Label: "Upload tool name", Type: connector.FieldString, Default: "uploadFromUrlTool"},
		},
	}, New)
}

type Publisher struct {
	cfg  connector.Config
	opts mcpx.Options
	now  func() time.Time
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	opts := mcpx.Options{
		Transport:  cfg.String("transport"),
		Endpoint:   cfg.String("endpoint_url"),
		Command:    cfg.String("command"),
		Args:       cfg.StringList("args"),
		HTTPClient: env.HTTPClient,
	}
	if k := cfg.String("api_key"); k != "" {
		opts.Headers = map[string]string{"Authorization": "Bearer " + k}
		opts.Env = map[string]string{"POSTIZ_API_KEY": k}
	}
	return &Publisher{cfg: cfg, opts: opts, now: time.Now}, nil
}

func (p *Publisher) connect(ctx context.Context) (*mcp.ClientSession, error) {
	return mcpx.Connect(ctx, p.opts)
}

func (p *Publisher) Test(ctx context.Context) (*connector.TestResult, error) {
	cs, err := p.connect(ctx)
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	defer cs.Close()
	tools, err := mcpx.ListTools(ctx, cs)
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	names := make([]string, 0, len(tools))
	has := map[string]bool{}
	for _, t := range tools {
		names = append(names, t.Name)
		has[t.Name] = true
	}
	if !has[p.cfg.String("schedule_tool")] {
		return &connector.TestResult{OK: false,
			Message: fmt.Sprintf("connected, but tool %q is missing (server has: %s)", p.cfg.String("schedule_tool"), strings.Join(names, ", ")),
			Details: map[string]any{"tools": names}}, nil
	}
	channels, err := p.channels(ctx, cs)
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error(), Details: map[string]any{"tools": names}}, nil
	}
	var list []string
	for _, c := range channels {
		if !c.Disabled {
			list = append(list, fmt.Sprintf("%s (%s)", c.Name, c.Platform))
		}
	}
	return &connector.TestResult{OK: true,
		Message: fmt.Sprintf("connected: %d tools, %d active channels", len(tools), len(list)),
		Details: map[string]any{"tools": names, "channels": list}}, nil
}

// ListTools shows the server's tools. Publisher tools are never offered to the agent.
func (p *Publisher) ListTools(ctx context.Context) ([]connector.ToolInfo, error) {
	cs, err := p.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer cs.Close()
	tools, err := mcpx.ListTools(ctx, cs)
	if err != nil {
		return nil, err
	}
	out := make([]connector.ToolInfo, 0, len(tools))
	for _, t := range tools {
		out = append(out, connector.ToolInfo{Name: t.Name, Description: t.Description, AgentAllowed: false,
			Reason: "publisher tools are only invoked by the dispatch worker for approved posts"})
	}
	return out, nil
}

type channel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	Identifier string `json:"identifier"`
	Disabled   bool   `json:"disabled"`
}

func (p *Publisher) channels(ctx context.Context, cs *mcp.ClientSession) ([]channel, error) {
	raw, err := mcpx.CallJSON(ctx, cs, p.cfg.String("list_tool"), map[string]any{})
	if err != nil {
		return nil, err
	}
	var wrapped struct {
		Output []channel `json:"output"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Output != nil {
		return wrapped.Output, nil
	}
	var direct []channel
	if err := json.Unmarshal(raw, &direct); err != nil {
		return nil, fmt.Errorf("postiz: unexpected %s result: %.200s", p.cfg.String("list_tool"), raw)
	}
	return direct, nil
}

// platformAliases maps CueFlow platform IDs to Postiz provider identifiers.
var platformAliases = map[string][]string{
	"threads":   {"threads"},
	"instagram": {"instagram", "instagram-standalone"},
	"x":         {"x", "twitter"},
	"linkedin":  {"linkedin", "linkedin-page"},
}

func matchChannel(channels []channel, platform string) (channel, bool) {
	aliases := platformAliases[platform]
	if aliases == nil {
		aliases = []string{platform}
	}
	for _, alias := range aliases {
		for _, c := range channels {
			if c.Disabled {
				continue
			}
			if strings.EqualFold(c.Platform, alias) || strings.EqualFold(c.Identifier, alias) {
				return c, true
			}
		}
	}
	return channel{}, false
}

type postItem struct {
	Content     string   `json:"content"`
	Attachments []string `json:"attachments"`
}

type setting struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

type socialPost struct {
	IntegrationID    string     `json:"integrationId"`
	IsPremium        bool       `json:"isPremium"`
	Date             string     `json:"date"`
	ShortLink        bool       `json:"shortLink"`
	Type             string     `json:"type"`
	PostsAndComments []postItem `json:"postsAndComments"`
	Settings         []setting  `json:"settings"`
}

func (p *Publisher) Publish(ctx context.Context, req connector.PublishRequest) (*connector.PublishResult, error) {
	cs, err := p.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer cs.Close()

	mapping := p.cfg.KV("integrations")
	var listed []channel
	integrationFor := map[string]string{} // platform -> integration id
	for _, pl := range req.Platforms {
		if id := mapping[pl]; id != "" {
			integrationFor[pl] = id
			continue
		}
		if listed == nil {
			if listed, err = p.channels(ctx, cs); err != nil {
				return nil, err
			}
		}
		ch, ok := matchChannel(listed, pl)
		if !ok {
			return nil, fmt.Errorf("postiz: no active %s channel connected in Postiz (add one or set the channel mapping)", pl)
		}
		integrationFor[pl] = ch.ID
	}

	media, err := p.uploadMedia(ctx, cs, req.MediaURLs)
	if err != nil {
		return nil, err
	}
	payload := p.BuildPayload(req, integrationFor, media)

	raw, err := mcpx.CallJSON(ctx, cs, p.cfg.String("schedule_tool"), payload)
	if err != nil {
		return nil, err
	}
	return parseScheduleResult(raw, integrationFor)
}

// BuildPayload assembles the integrationSchedulePostTool arguments.
func (p *Publisher) BuildPayload(req connector.PublishRequest, integrationFor map[string]string, media []string) map[string]any {
	date := p.now().UTC()
	typ := "now"
	if req.ScheduledAt != nil && req.ScheduledAt.After(date) {
		date = req.ScheduledAt.UTC()
		typ = "schedule"
	}
	if p.cfg.String("post_mode") == "draft" {
		typ = "draft"
	}
	if media == nil {
		media = []string{}
	}
	tags := domain.FormatHashtags(req.Hashtags)

	posts := make([]socialPost, 0, len(req.Platforms))
	for _, pl := range req.Platforms {
		info, _ := domain.PlatformByID(pl)
		parts := req.Parts
		if !info.SupportsThread && len(parts) > 1 {
			parts = []string{strings.Join(parts, "\n\n")}
		}
		items := make([]postItem, 0, len(parts))
		for i, part := range parts {
			text := part
			if i == len(parts)-1 && tags != "" {
				text += "\n\n" + tags
			}
			attachments := []string{}
			if i == 0 {
				attachments = media
			}
			items = append(items, postItem{Content: textutil.ToHTMLParagraphs(text), Attachments: attachments})
		}
		settings := []setting{}
		if pl == "instagram" {
			settings = append(settings, setting{Key: "post_type", Value: p.cfg.String("instagram_post_type")})
		}
		posts = append(posts, socialPost{
			IntegrationID:    integrationFor[pl],
			Date:             date.Format(time.RFC3339),
			ShortLink:        p.cfg.Bool("shorten_links"),
			Type:             typ,
			PostsAndComments: items,
			Settings:         settings,
		})
	}
	return map[string]any{"socialPost": posts}
}

func (p *Publisher) uploadMedia(ctx context.Context, cs *mcp.ClientSession, urls []string) ([]string, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	if !p.cfg.Bool("upload_media") {
		return urls, nil
	}
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		raw, err := mcpx.CallJSON(ctx, cs, p.cfg.String("upload_tool"), map[string]any{"url": u})
		if err != nil {
			return nil, fmt.Errorf("postiz: upload %s: %w", u, err)
		}
		var res struct {
			ID     string `json:"id"`
			Path   string `json:"path"`
			Error  string `json:"error"`
			Output *struct {
				Path string `json:"path"`
			} `json:"output"`
		}
		_ = json.Unmarshal(raw, &res)
		switch {
		case res.Error != "":
			return nil, fmt.Errorf("postiz: upload %s: %s", u, res.Error)
		case res.Path != "":
			out = append(out, res.Path)
		case res.Output != nil && res.Output.Path != "":
			out = append(out, res.Output.Path)
		default:
			return nil, fmt.Errorf("postiz: upload %s: unexpected result %.200s", u, raw)
		}
	}
	return out, nil
}

// parseScheduleResult maps Postiz's response back to platform post IDs. Postiz reports validation
// failures as a successful tool call shaped {output:{errors:"..."}}, so that is checked explicitly.
func parseScheduleResult(raw json.RawMessage, integrationFor map[string]string) (*connector.PublishResult, error) {
	var env struct {
		Output json.RawMessage `json:"output"`
		Errors any             `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("postiz: unexpected result: %.300s", raw)
	}
	if env.Errors != nil {
		return nil, fmt.Errorf("postiz: %v", env.Errors)
	}
	var failed struct {
		Errors any `json:"errors"`
	}
	if json.Unmarshal(env.Output, &failed) == nil && failed.Errors != nil {
		return nil, fmt.Errorf("postiz rejected the post: %v", failed.Errors)
	}
	var created []struct {
		PostID      string `json:"postId"`
		Integration string `json:"integration"`
		PreviewURL  string `json:"previewUrl"`
	}
	if err := json.Unmarshal(env.Output, &created); err != nil || len(created) == 0 {
		return nil, fmt.Errorf("postiz: no post was created: %.300s", raw)
	}
	byIntegration := map[string]string{}
	for pl, id := range integrationFor {
		byIntegration[id] = pl
	}
	res := &connector.PublishResult{ExternalIDs: map[string]string{}, Details: map[string]any{}}
	previews := map[string]string{}
	for i, c := range created {
		pl := byIntegration[c.Integration]
		if pl == "" {
			pl = fmt.Sprintf("post_%d", i+1)
		}
		res.ExternalIDs[pl] = c.PostID
		if c.PreviewURL != "" {
			previews[pl] = c.PreviewURL
		}
	}
	if len(previews) > 0 {
		res.Details["preview_urls"] = previews
	}
	return res, nil
}
