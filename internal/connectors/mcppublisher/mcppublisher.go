// Package mcppublisher is a publisher connector for any MCP server that follows the publisher
// contract below: a scheduler that posts to several Threads and Instagram accounts, including
// threads and multi-image posts. CueFlow calls the server's MCP tools; the reviewer picks the
// accounts for each post (PublishRequest.Accounts). docs/publisher-mcp.md describes the contract.
//
// The server speaks MCP over Streamable HTTP (stateless is fine). Requests carry whatever the
// connector is given: a bearer token, extra headers, and secret headers (a Cloudflare Access service
// token, say). Tools used (their names can be changed in the settings):
//   - list_accounts {}                                  -> {accounts:[{id, platform, username, tag?}], platforms}
//   - create_upload {content_type, size}                -> {upload_url, method, headers, key} (signed, 15 min; optional)
//   - create_post {parts:[{text, image_urls?, images_base64?, video_urls?, video_keys?}], accounts[], scheduled_at?, topic?, link?}
//     (several parts make a Threads reply chain; on Instagram they become one caption and carousel)
//     -> {id, status: scheduled|published|partial|failed, results:[{account, label, published, permalink, error}]}
//   - publish_post {id}                                 -> same shape; retries the accounts not yet reached
//
// The contract has no idempotency key, so a create_post that gets no reply is reported as failed
// with a warning to check the publisher before retrying: it may already have gone out. A post that
// was created but did not reach every account is retried with publish_post, which only republishes
// the accounts that failed.
//
// The connector was first written for CueSocial and is still registered under that type, hidden,
// so installs that have one keep working.
package mcppublisher

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/mcpx"
)

// PostIDKey is the ExternalIDs key holding the publisher's post id. Posts made while this was
// still the CueSocial connector keep their id under legacyPostIDKey, which a retry still finds.
const (
	PostIDKey       = "post_id"
	legacyPostIDKey = "cuesocial"
)

// Default tool names; each can be changed in the connector's settings.
const (
	defaultListTool    = "list_accounts"
	defaultCreateTool  = "create_post"
	defaultPublishTool = "publish_post"
	defaultUploadTool  = "create_upload"
)

func init() {
	connector.Register(spec(), New)
	// The type this connector was first registered under, for installs that already have one. Its
	// settings are the same, so a stored connector of this type works unchanged.
	legacy := spec()
	legacy.Type, legacy.Name, legacy.Hidden = "cuesocial", "CueSocial", true
	connector.Register(legacy, New)
}

func spec() connector.Spec {
	return connector.Spec{
		Type:             "mcp_publisher",
		Kind:             connector.KindPublisher,
		Name:             "MCP publisher",
		Description:      "Publishes approved posts to your Threads and Instagram accounts through an MCP server that follows the publisher contract (docs/publisher-mcp.md). Choose the accounts for each post when you review it.",
		Accounts:         true,
		AccountsRequired: true,
		Fields: []connector.Field{
			{Key: "endpoint_url", Label: "MCP endpoint", Type: connector.FieldString, Required: true,
				Placeholder: "https://social.example.com/mcp", Help: "The publisher's URL + /mcp"},
			{Key: "auth_token", Label: "Bearer token", Type: connector.FieldPassword, Secret: true,
				Help: "Sent as Authorization: Bearer <token>"},
			{Key: "headers", Label: "Extra headers", Type: connector.FieldKV},
			{Key: "secret_headers", Label: "Secret headers", Type: connector.FieldText, Secret: true,
				Help: "Header-Name: value, one per line. For a Cloudflare Access service token: CF-Access-Client-Id and CF-Access-Client-Secret"},
			{Key: "list_tool", Label: "List-accounts tool", Type: connector.FieldString, Default: defaultListTool},
			{Key: "create_tool", Label: "Create-post tool", Type: connector.FieldString, Default: defaultCreateTool},
			{Key: "publish_tool", Label: "Publish-post tool", Type: connector.FieldString, Default: defaultPublishTool,
				Help: "Retries the accounts a post has not reached yet"},
			{Key: "upload_tool", Label: "Upload tool", Type: connector.FieldString, Default: defaultUploadTool,
				Help: "Takes video uploads; if the server has none, videos need the public media URL below"},
			{Key: "public_media_base_url", Label: "Public media URL", Type: connector.FieldString, Placeholder: "https://media.example.com",
				Help: "Optional. Videos are uploaded straight to the publisher; this public https address (e.g. a Cloudflare Tunnel exposing only /media/) is only used when it can't take uploads"},
			{Key: "cf_client_id", Label: "Cloudflare Access client ID (older setting)", Type: connector.FieldString,
				Help: "Kept for connectors made before secret headers existed; use Secret headers for new ones"},
			{Key: "cf_client_secret", Label: "Cloudflare Access client secret (older setting)", Type: connector.FieldPassword, Secret: true},
		},
		DefaultName: func(cfg connector.Config) string {
			if u, err := url.Parse(cfg.String("endpoint_url")); err == nil && u.Host != "" {
				return connector.WithDetail("MCP publisher", u.Host)
			}
			return ""
		},
	}
}

type Publisher struct {
	opts        mcpx.Options
	now         func() time.Time
	publicMedia string       // base URL the publisher can fetch stored videos from (fallback)
	upload      *http.Client // sends videos to the publisher's signed upload URLs
	tools       tools
}

type tools struct{ list, create, publish, upload string }

// previousID is the publisher's post id from an earlier, partly successful attempt, whether it was
// recorded under the current key or the one the CueSocial connector used.
func previousID(previous map[string]string) string {
	if id := previous[PostIDKey]; id != "" {
		return id
	}
	return previous[legacyPostIDKey]
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	opts := mcpx.Options{Transport: mcpx.TransportStreamable, Endpoint: cfg.String("endpoint_url"), HTTPClient: env.HTTPClient,
		Headers: map[string]string{}}
	if opts.Endpoint == "" {
		return nil, errors.New("mcp publisher: an MCP endpoint is required")
	}
	if id := cfg.String("cf_client_id"); id != "" {
		opts.Headers["CF-Access-Client-Id"] = id
		opts.Headers["CF-Access-Client-Secret"] = cfg.String("cf_client_secret")
	}
	for k, v := range cfg.KV("headers") {
		opts.Headers[k] = v
	}
	sc := bufio.NewScanner(strings.NewReader(cfg.String("secret_headers")))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("mcp publisher: secret header line %q is not Header-Name: value", line)
		}
		opts.Headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if t := cfg.String("auth_token"); t != "" {
		opts.Headers["Authorization"] = "Bearer " + t
	}
	name := func(key, def string) string {
		if n := strings.TrimSpace(cfg.String(key)); n != "" {
			return n
		}
		return def
	}
	upload := &http.Client{}
	if env.HTTPClient != nil {
		*upload = *env.HTTPClient
	}
	upload.Timeout = 15 * time.Minute // a Reel is tens of MB
	return &Publisher{opts: opts, now: time.Now, upload: upload,
		publicMedia: strings.TrimRight(strings.TrimSpace(cfg.String("public_media_base_url")), "/"),
		tools: tools{list: name("list_tool", defaultListTool), create: name("create_tool", defaultCreateTool),
			publish: name("publish_tool", defaultPublishTool), upload: name("upload_tool", defaultUploadTool)}}, nil
}

type account struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	Username string `json:"username"`
	Tag      string `json:"tag"` // brand, personal or test
}

func (a account) label() string { return a.Platform + " @" + a.Username }

type accountsOutput struct {
	Accounts  []account `json:"accounts"`
	Platforms []struct {
		Platform   string `json:"platform"`
		Configured bool   `json:"configured"`
	} `json:"platforms"`
}

type result struct {
	Account   string `json:"account"`
	Label     string `json:"label"`
	Published bool   `json:"published"`
	Permalink string `json:"permalink"`
	Error     string `json:"error"`
}

type postView struct {
	ID      string   `json:"id"`
	Status  string   `json:"status"`
	Results []result `json:"results"`
}

func (p *Publisher) accounts(ctx context.Context, cs *mcp.ClientSession) (*accountsOutput, error) {
	raw, err := mcpx.CallJSON(ctx, cs, p.tools.list, map[string]any{})
	if err != nil {
		return nil, err
	}
	var out accountsOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mcp publisher: list accounts: %w", err)
	}
	return &out, nil
}

func (p *Publisher) Test(ctx context.Context) (*connector.TestResult, error) {
	cs, err := mcpx.Connect(ctx, p.opts)
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	defer cs.Close()
	out, err := p.accounts(ctx, cs)
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	labels := make([]string, 0, len(out.Accounts))
	for _, a := range out.Accounts {
		labels = append(labels, a.label())
	}
	if len(labels) == 0 {
		return &connector.TestResult{OK: false, Message: "connected, but the publisher has no accounts connected yet"}, nil
	}
	return &connector.TestResult{OK: true, Message: fmt.Sprintf("connected: %d accounts (%s)", len(labels), strings.Join(labels, ", ")),
		Details: map[string]any{"accounts": labels}}, nil
}

// ListAccounts offers the publisher's connected accounts to the review UI.
func (p *Publisher) ListAccounts(ctx context.Context) ([]connector.Account, error) {
	cs, err := mcpx.Connect(ctx, p.opts)
	if err != nil {
		return nil, err
	}
	defer cs.Close()
	out, err := p.accounts(ctx, cs)
	if err != nil {
		return nil, err
	}
	accounts := make([]connector.Account, 0, len(out.Accounts))
	for _, a := range out.Accounts {
		accounts = append(accounts, connector.Account{ID: a.ID, Platform: a.Platform, Name: "@" + a.Username, Tag: a.Tag})
	}
	return accounts, nil
}

func (p *Publisher) Publish(ctx context.Context, req connector.PublishRequest) (*connector.PublishResult, error) {
	cs, err := mcpx.Connect(ctx, p.opts)
	if err != nil {
		return nil, err
	}
	defer cs.Close()

	if id := previousID(req.Previous); id != "" {
		// An earlier attempt created the post but did not reach every account: finish it.
		raw, err := mcpx.CallJSON(ctx, cs, p.tools.publish, map[string]any{"id": id})
		if err != nil {
			return nil, sentErr(p.tools.publish, err, map[string]string{PostIDKey: id})
		}
		return finish(raw, req.Previous)
	}

	args, err := p.createArgs(ctx, cs, req)
	if err != nil {
		return nil, err
	}
	raw, err := mcpx.CallJSON(ctx, cs, p.tools.create, args)
	if err != nil {
		return nil, sentErr(p.tools.create, err, nil)
	}
	return finish(raw, nil)
}

// createArgs checks the post against the chosen accounts and builds create_post's arguments.
func (p *Publisher) createArgs(ctx context.Context, cs *mcp.ClientSession, req connector.PublishRequest) (map[string]any, error) {
	if len(req.Accounts) == 0 {
		return nil, errors.New("no accounts chosen for this post: pick them in the review editor, then retry")
	}
	out, err := p.accounts(ctx, cs)
	if err != nil {
		return nil, err
	}
	byID := map[string]account{}
	for _, a := range out.Accounts {
		byID[a.ID] = a
	}
	platforms := map[string]bool{}
	tags := map[bool]bool{} // is test account -> seen
	for _, id := range req.Accounts {
		a, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("account %s is no longer connected: pick the accounts again", id)
		}
		if !slices.Contains(req.Platforms, a.Platform) {
			return nil, fmt.Errorf("account %s is on %s, which is not one of this post's platforms", a.label(), a.Platform)
		}
		platforms[a.Platform] = true
		tags[a.Tag == "test"] = true
	}
	if tags[true] && tags[false] {
		return nil, errors.New("test accounts (tagged \"test\") are never mixed with real ones in one post: pick only test accounts or only real ones")
	}

	parts, err := partsFor(ctx, req, platforms, p.publicMedia, func(ctx context.Context, data []byte, mime string) (string, error) {
		return p.uploadVideo(ctx, cs, data, mime)
	})
	if err != nil {
		return nil, err
	}
	args := map[string]any{"parts": parts, "accounts": req.Accounts}
	topic, link, err := extras(req, parts, platforms)
	if err != nil {
		return nil, err
	}
	if topic != "" {
		args["topic"] = topic
	}
	if link != "" {
		args["link"] = link
	}
	if req.ScheduledAt != nil && req.ScheduledAt.After(p.now()) {
		args["scheduled_at"] = req.ScheduledAt.UTC().Format(time.RFC3339)
	}
	return args, nil
}

// Limits of the publisher contract (docs/publisher-mcp.md).
const (
	maxParts            = 10
	threadsMaxMedia     = 20
	instagramMaxMedia   = 10
	maxImageBytes       = 8 << 20
	threadsMaxChars     = 500
	instagramMaxCaption = 2200
)

// partsFor builds create_post's parts: CueFlow's thread parts in order, hashtags after the last
// part, and every image and video on the first part (an album on Threads; a carousel on Instagram,
// or a reel when it is a single video). Images CueFlow stores go as base64 and its videos are
// uploaded to the publisher's storage (the upload tool); linked images and videos are fetched by the publisher.
func partsFor(ctx context.Context, req connector.PublishRequest, platforms map[string]bool, publicMedia string,
	upload func(ctx context.Context, data []byte, mime string) (string, error)) ([]map[string]any, error) {
	texts := append([]string{}, req.Parts...)
	if len(texts) == 0 {
		texts = []string{""}
	}
	if len(texts) > maxParts {
		return nil, fmt.Errorf("threads have at most %d parts; this post has %d", maxParts, len(texts))
	}
	if tags := domain.FormatHashtags(req.Hashtags); tags != "" {
		texts[len(texts)-1] = strings.TrimSpace(texts[len(texts)-1] + "\n\n" + tags)
	}
	if platforms["threads"] {
		for i, t := range texts {
			if n := utf8.RuneCountInString(t); n > threadsMaxChars {
				return nil, fmt.Errorf("Threads allows %d characters per part; part %d has %d (split it into thread parts)", threadsMaxChars, i+1, n)
			}
		}
	}
	if platforms["instagram"] {
		if n := utf8.RuneCountInString(strings.Join(texts, "\n\n")); n > instagramMaxCaption {
			return nil, fmt.Errorf("Instagram captions allow %d characters; this post's parts add up to %d", instagramMaxCaption, n)
		}
	}

	var urls, b64, videos, videoKeys []string
	for i, u := range req.MediaURLs {
		u = strings.TrimSpace(u)
		stored := i < len(req.Media) && req.Media[i].Stored
		switch {
		case isVideoURL(u) && stored:
			data, mt, err := req.Media[i].Load(ctx)
			if err != nil {
				return nil, fmt.Errorf("load video %d: %w", i+1, err)
			}
			key, err := upload(ctx, data, mt)
			if err == nil {
				videoKeys = append(videoKeys, key)
				break
			}
			var te *mcpx.ToolError
			if !errors.As(err, &te) || publicMedia == "" {
				return nil, fmt.Errorf("upload video %d to the publisher: %w", i+1, err)
			}
			// This publisher can't take uploads: let it fetch the video from the public media URL.
			pu, perr := url.Parse(u)
			if perr != nil {
				return nil, fmt.Errorf("media %d: %w", i+1, perr)
			}
			videos = append(videos, publicMedia+pu.Path)
		case isVideoURL(u):
			if !strings.HasPrefix(u, "https://") {
				return nil, fmt.Errorf("the publisher fetches videos itself and needs a public https URL, got %q", u)
			}
			videos = append(videos, u)
		case stored:
			data, mt, err := req.Media[i].Load(ctx)
			if err != nil {
				return nil, fmt.Errorf("load image %d: %w", i+1, err)
			}
			if mt != "image/jpeg" && mt != "image/png" {
				return nil, fmt.Errorf("image %d is %s; the publisher accepts JPEG or PNG", i+1, mt)
			}
			if len(data) > maxImageBytes {
				return nil, fmt.Errorf("image %d is larger than the 8 MB limit", i+1)
			}
			b64 = append(b64, base64.StdEncoding.EncodeToString(data))
		default:
			if !strings.HasPrefix(u, "https://") {
				return nil, fmt.Errorf("the publisher fetches linked images itself and needs a public https URL, got %q (upload the image to CueFlow instead)", u)
			}
			urls = append(urls, u)
		}
	}
	media := len(urls) + len(b64) + len(videos) + len(videoKeys)
	if platforms["instagram"] && media == 0 {
		return nil, errors.New("Instagram needs at least one image or video: add one, or untick the Instagram accounts")
	}
	if platforms["instagram"] && media > instagramMaxMedia {
		return nil, fmt.Errorf("Instagram posts take at most %d images and videos; this post has %d", instagramMaxMedia, media)
	}
	if platforms["threads"] && media > threadsMaxMedia {
		return nil, fmt.Errorf("Threads posts take at most %d images and videos; this post has %d", threadsMaxMedia, media)
	}

	parts := make([]map[string]any, len(texts))
	for i, t := range texts {
		parts[i] = map[string]any{"text": t}
	}
	// Within a part the publisher orders linked images, uploaded images, then videos.
	if len(urls) > 0 {
		parts[0]["image_urls"] = urls
	}
	if len(b64) > 0 {
		parts[0]["images_base64"] = b64
	}
	if len(videos) > 0 {
		parts[0]["video_urls"] = videos
	}
	if len(videoKeys) > 0 {
		parts[0]["video_keys"] = videoKeys
	}
	return parts, nil
}

// uploadVideo sends a video to a signed URL from the upload tool and returns its key.
func (p *Publisher) uploadVideo(ctx context.Context, cs *mcp.ClientSession, data []byte, mime string) (string, error) {
	raw, err := mcpx.CallJSON(ctx, cs, p.tools.upload, map[string]any{"content_type": mime, "size": len(data)})
	if err != nil {
		return "", err
	}
	var up struct {
		UploadURL string            `json:"upload_url"`
		Method    string            `json:"method"`
		Headers   map[string]string `json:"headers"`
		Key       string            `json:"key"`
	}
	if err := json.Unmarshal(raw, &up); err != nil || up.UploadURL == "" || up.Key == "" {
		return "", fmt.Errorf("unexpected create_upload reply: %.200s", raw)
	}
	method := up.Method
	if method == "" {
		method = http.MethodPut
	}
	hreq, err := http.NewRequestWithContext(ctx, method, up.UploadURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	hreq.ContentLength = int64(len(data))
	for k, v := range up.Headers {
		hreq.Header.Set(k, v)
	}
	resp, err := p.upload.Do(hreq)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return "", fmt.Errorf("upload: HTTP %d %s", resp.StatusCode, body)
	}
	return up.Key, nil
}

// isVideoURL recognizes video links by extension (CueFlow serves stored videos as /media/{id}.mp4).
func isVideoURL(u string) bool {
	pu, err := url.Parse(u)
	if err != nil {
		return false
	}
	p := strings.ToLower(pu.Path)
	return strings.HasSuffix(p, ".mp4") || strings.HasSuffix(p, ".mov")
}

const threadsMaxTopic = 50

// extras checks the Threads topic tag and link card. The publisher puts the topic on the first part and
// the link card on the last part without images; Instagram ignores both.
func extras(req connector.PublishRequest, parts []map[string]any, platforms map[string]bool) (string, string, error) {
	if !platforms["threads"] {
		return "", "", nil // Threads-only features; Instagram ignores them
	}
	topic := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(req.Topic), "#"))
	if topic != "" {
		if n := utf8.RuneCountInString(topic); n > threadsMaxTopic {
			return "", "", fmt.Errorf("a Threads topic is at most %d characters; %q has %d", threadsMaxTopic, topic, n)
		}
		if strings.ContainsAny(topic, ".&") {
			return "", "", fmt.Errorf("a Threads topic can't contain . or &: %q", topic)
		}
	}
	link := strings.TrimSpace(req.Link)
	if link != "" {
		u, err := url.Parse(link)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", "", fmt.Errorf("the link card needs a full http(s) URL, got %q", link)
		}
		textOnly := false
		for _, p := range parts {
			if p["image_urls"] == nil && p["images_base64"] == nil && p["video_urls"] == nil && p["video_keys"] == nil {
				textOnly = true
			}
		}
		if !textOnly {
			return "", "", errors.New("a Threads link card needs a part without images or videos: add a text-only thread part, or clear the link card")
		}
	}
	return topic, link, nil
}

// sentErr classifies a failed call. A tool error means the publisher rejected the request, so nothing
// went out. Any other failure (timeout, dropped connection) leaves the outcome unknown.
func sentErr(tool string, err error, known map[string]string) error {
	var te *mcpx.ToolError
	if errors.As(err, &te) {
		if known != nil {
			return &connector.PartialError{ExternalIDs: known, Err: err}
		}
		return err
	}
	err = fmt.Errorf("no reply from the publisher to %s (%v): the post may have been published; check the publisher before retrying", tool, err)
	if known != nil {
		// publish_post only retries the accounts not yet reached, so retrying it stays safe.
		return &connector.PartialError{ExternalIDs: known, Err: err}
	}
	return err
}

// finish turns the publisher's post view into a result, or a PartialError that keeps the post id
// (and what did go out) for publish_post on retry.
func finish(raw json.RawMessage, previous map[string]string) (*connector.PublishResult, error) {
	var v postView
	if err := json.Unmarshal(raw, &v); err != nil || v.ID == "" {
		return nil, fmt.Errorf("mcp publisher: unexpected reply %s", truncate(string(raw), 300))
	}
	ids := map[string]string{}
	for k, val := range previous {
		ids[k] = val
	}
	ids[PostIDKey] = v.ID
	var failed []string
	for _, r := range v.Results {
		name := r.Label
		if name == "" {
			name = r.Account
		}
		if r.Published {
			ids[name] = r.Permalink
		} else if r.Error != "" {
			failed = append(failed, name+": "+r.Error)
		}
	}
	switch v.Status {
	case "published", "scheduled", "publishing":
		return &connector.PublishResult{ExternalIDs: ids, Details: map[string]any{"status": v.Status}}, nil
	}
	msg := fmt.Sprintf("post %s is %s", v.ID, v.Status)
	if len(failed) > 0 {
		msg += ": " + strings.Join(failed, "; ")
	}
	return nil, &connector.PartialError{ExternalIDs: ids, Err: errors.New(msg + " (retry publishes only the accounts that failed)")}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
