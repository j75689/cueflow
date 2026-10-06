// Package cuesocial is the publisher connector for CueSocial, a self-hosted scheduler that posts
// to several Threads and Instagram accounts, including threads and multi-image posts. CueFlow calls
// CueSocial's MCP server; the reviewer picks the accounts for each post (PublishRequest.Accounts).
//
// CueSocial MCP (Streamable HTTP, stateless) sits behind Cloudflare Access, so requests carry a
// service token in CF-Access-Client-Id / CF-Access-Client-Secret. Tools used:
//   - list_accounts {}                                  -> {accounts:[{id, platform, username}], platforms}
//   - create_post {parts:[{text, image_urls?, images_base64?}], accounts[], scheduled_at?, topic?, link?}
//     (several parts make a Threads reply chain; on Instagram they become one caption and carousel)
//     -> {id, status: scheduled|published|partial|failed, results:[{account, label, published, permalink, error}]}
//   - publish_post {id}                                 -> same shape; retries the accounts not yet reached
//
// CueSocial has no idempotency key, so a create_post that gets no reply is reported as failed
// with a warning to check CueSocial before retrying: it may already have gone out. A post that
// was created but did not reach every account is retried with publish_post, which only republishes
// the accounts that failed.
package cuesocial

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

// PostIDKey is the ExternalIDs key holding the CueSocial post id.
const PostIDKey = "cuesocial"

func init() {
	connector.Register(connector.Spec{
		Type:             "cuesocial",
		Kind:             connector.KindPublisher,
		Name:             "CueSocial",
		Description:      "Publishes approved posts to your Threads and Instagram accounts through a CueSocial server. Choose the accounts for each post when you review it.",
		Accounts:         true,
		AccountsRequired: true,
		Fields: []connector.Field{
			{Key: "endpoint_url", Label: "MCP endpoint", Type: connector.FieldString, Required: true,
				Placeholder: "https://social.example.com/mcp", Help: "Your CueSocial URL + /mcp"},
			{Key: "cf_client_id", Label: "Cloudflare Access client ID", Type: connector.FieldString,
				Help: "Service token allowed by the CueSocial Access application (CF-Access-Client-Id)"},
			{Key: "cf_client_secret", Label: "Cloudflare Access client secret", Type: connector.FieldPassword, Secret: true,
				Help: "CF-Access-Client-Secret of the same service token"},
		},
		DefaultName: func(cfg connector.Config) string {
			if u, err := url.Parse(cfg.String("endpoint_url")); err == nil && u.Host != "" {
				return connector.WithDetail("CueSocial", u.Host)
			}
			return ""
		},
	}, New)
}

type Publisher struct {
	opts mcpx.Options
	now  func() time.Time
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	opts := mcpx.Options{Transport: mcpx.TransportStreamable, Endpoint: cfg.String("endpoint_url"), HTTPClient: env.HTTPClient}
	if id := cfg.String("cf_client_id"); id != "" {
		opts.Headers = map[string]string{"CF-Access-Client-Id": id, "CF-Access-Client-Secret": cfg.String("cf_client_secret")}
	}
	return &Publisher{opts: opts, now: time.Now}, nil
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
	raw, err := mcpx.CallJSON(ctx, cs, "list_accounts", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out accountsOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("cuesocial: list_accounts: %w", err)
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
		return &connector.TestResult{OK: false, Message: "connected, but no accounts are connected in CueSocial yet"}, nil
	}
	return &connector.TestResult{OK: true, Message: fmt.Sprintf("connected: %d accounts (%s)", len(labels), strings.Join(labels, ", ")),
		Details: map[string]any{"accounts": labels}}, nil
}

// ListAccounts offers CueSocial's connected accounts to the review UI.
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

	if id := req.Previous[PostIDKey]; id != "" {
		// An earlier attempt created the post but did not reach every account: finish it.
		raw, err := mcpx.CallJSON(ctx, cs, "publish_post", map[string]any{"id": id})
		if err != nil {
			return nil, sentErr("publish_post", err, map[string]string{PostIDKey: id})
		}
		return finish(raw, req.Previous)
	}

	args, err := p.createArgs(ctx, cs, req)
	if err != nil {
		return nil, err
	}
	raw, err := mcpx.CallJSON(ctx, cs, "create_post", args)
	if err != nil {
		return nil, sentErr("create_post", err, nil)
	}
	return finish(raw, nil)
}

// createArgs checks the post against the chosen accounts and builds create_post's arguments.
func (p *Publisher) createArgs(ctx context.Context, cs *mcp.ClientSession, req connector.PublishRequest) (map[string]any, error) {
	if len(req.Accounts) == 0 {
		return nil, errors.New("no CueSocial accounts chosen for this post: pick them in the review editor, then retry")
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
	for _, id := range req.Accounts {
		a, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("CueSocial account %s is no longer connected: pick the accounts again", id)
		}
		if !slices.Contains(req.Platforms, a.Platform) {
			return nil, fmt.Errorf("account %s is on %s, which is not one of this post's platforms", a.label(), a.Platform)
		}
		platforms[a.Platform] = true
	}

	parts, err := partsFor(ctx, req, platforms)
	if err != nil {
		return nil, err
	}
	args := map[string]any{"parts": parts, "accounts": req.Accounts}
	topic, link, err := extras(req, parts)
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

// CueSocial's limits (see its MCP instructions).
const (
	maxParts            = 10
	threadsMaxImages    = 20
	instagramMaxImages  = 10
	maxImageBytes       = 8 << 20
	threadsMaxChars     = 500
	instagramMaxCaption = 2200
)

// partsFor builds create_post's parts: CueFlow's thread parts in order, hashtags after the last
// part, and every image on the first part (a carousel on Instagram, an album on Threads). Images
// CueFlow stores are uploaded as base64; linked images are fetched by CueSocial.
func partsFor(ctx context.Context, req connector.PublishRequest, platforms map[string]bool) ([]map[string]any, error) {
	texts := append([]string{}, req.Parts...)
	if len(texts) == 0 {
		texts = []string{""}
	}
	if len(texts) > maxParts {
		return nil, fmt.Errorf("CueSocial threads have at most %d parts; this post has %d", maxParts, len(texts))
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

	var urls, b64 []string
	for i, u := range req.MediaURLs {
		if i < len(req.Media) && req.Media[i].Stored {
			data, mt, err := req.Media[i].Load(ctx)
			if err != nil {
				return nil, fmt.Errorf("load image %d: %w", i+1, err)
			}
			if strings.HasPrefix(mt, "video/") {
				return nil, fmt.Errorf("media %d is a video; this CueSocial connector can only publish images so far", i+1)
			}
			if mt != "image/jpeg" && mt != "image/png" {
				return nil, fmt.Errorf("image %d is %s; CueSocial accepts JPEG or PNG", i+1, mt)
			}
			if len(data) > maxImageBytes {
				return nil, fmt.Errorf("image %d is larger than CueSocial's 8 MB limit", i+1)
			}
			b64 = append(b64, base64.StdEncoding.EncodeToString(data))
			continue
		}
		u = strings.TrimSpace(u)
		if !strings.HasPrefix(u, "https://") {
			return nil, fmt.Errorf("CueSocial fetches linked images itself and needs a public https URL, got %q (upload the image to CueFlow instead)", u)
		}
		urls = append(urls, u)
	}
	images := len(urls) + len(b64)
	if platforms["instagram"] && images == 0 {
		return nil, errors.New("Instagram needs at least one image: add one, or untick the Instagram accounts")
	}
	if platforms["instagram"] && images > instagramMaxImages {
		return nil, fmt.Errorf("Instagram carousels take at most %d images; this post has %d", instagramMaxImages, images)
	}
	if platforms["threads"] && images > threadsMaxImages {
		return nil, fmt.Errorf("Threads posts take at most %d images; this post has %d", threadsMaxImages, images)
	}

	parts := make([]map[string]any, len(texts))
	for i, t := range texts {
		parts[i] = map[string]any{"text": t}
	}
	// CueSocial puts a part's linked images before its uploaded ones.
	if len(urls) > 0 {
		parts[0]["image_urls"] = urls
	}
	if len(b64) > 0 {
		parts[0]["images_base64"] = b64
	}
	return parts, nil
}

const threadsMaxTopic = 50

// extras checks the Threads topic tag and link card. CueSocial puts the topic on the first part and
// the link card on the last part without images; Instagram ignores both.
func extras(req connector.PublishRequest, parts []map[string]any) (string, string, error) {
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
			if p["image_urls"] == nil && p["images_base64"] == nil {
				textOnly = true
			}
		}
		if !textOnly {
			return "", "", errors.New("a link card needs a part without images: add a text-only thread part for it")
		}
	}
	return topic, link, nil
}

// sentErr classifies a failed call. A tool error means CueSocial rejected the request, so nothing
// went out. Any other failure (timeout, dropped connection) leaves the outcome unknown.
func sentErr(tool string, err error, known map[string]string) error {
	var te *mcpx.ToolError
	if errors.As(err, &te) {
		if known != nil {
			return &connector.PartialError{ExternalIDs: known, Err: err}
		}
		return err
	}
	err = fmt.Errorf("no reply from CueSocial to %s (%v): the post may have been published; check CueSocial before retrying", tool, err)
	if known != nil {
		// publish_post only retries the accounts not yet reached, so retrying it stays safe.
		return &connector.PartialError{ExternalIDs: known, Err: err}
	}
	return err
}

// finish turns CueSocial's post view into a result, or a PartialError that keeps the post id
// (and what did go out) for publish_post on retry.
func finish(raw json.RawMessage, previous map[string]string) (*connector.PublishResult, error) {
	var v postView
	if err := json.Unmarshal(raw, &v); err != nil || v.ID == "" {
		return nil, fmt.Errorf("cuesocial: unexpected reply %s", truncate(string(raw), 300))
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
		return &connector.PublishResult{ExternalIDs: ids, Details: map[string]any{"cuesocial_status": v.Status}}, nil
	}
	msg := fmt.Sprintf("CueSocial post %s is %s", v.ID, v.Status)
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
