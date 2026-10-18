package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/deliverable"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/media"
)

// PostKind is the deliverable kind that carries a social post. It is the post flow on the
// deliverables rails: the same fields a post has, the platform rules as its validator, and
// publishing through the publisher connector as its action, under the deliverable's grant.
// Posts on the posts table keep working beside it; agents with the post_deliverables capability
// write here instead.
const PostKind = "post"

// PostPayload is the payload of a post deliverable. Its JSON matches the editable fields of Post,
// so the dashboard's post editor edits both.
type PostPayload struct {
	Content              string          `json:"content"`
	ThreadParts          []string        `json:"thread_parts"`
	Hashtags             []string        `json:"hashtags"`
	ImagePrompt          string          `json:"image_prompt,omitempty"`
	Platforms            []string        `json:"platforms"`
	MediaURLs            []string        `json:"media_urls"`
	Accounts             []string        `json:"accounts"`
	Topic                string          `json:"topic,omitempty"`
	LinkURL              string          `json:"link_url,omitempty"`
	ScheduledAt          *time.Time      `json:"scheduled_at,omitempty"`
	PublisherConnectorID *uuid.UUID      `json:"publisher_connector_id,omitempty"`
	SourceContext        json.RawMessage `json:"source_context,omitempty"`
	// Warnings are the platform rules the content bends; recomputed on every validation.
	Warnings []string `json:"warnings"`
}

// parts is the ordered list of posts to publish: thread parts when present, else the content.
func (p *PostPayload) parts() []string {
	if len(p.ThreadParts) > 0 {
		return p.ThreadParts
	}
	return []string{p.Content}
}

// asPost views the payload as a Post, for the rules that are written against one.
func (p *PostPayload) asPost() *Post {
	return &Post{Content: p.Content, ThreadParts: p.ThreadParts, Hashtags: p.Hashtags, Platforms: p.Platforms,
		MediaURLs: p.MediaURLs, Accounts: p.Accounts, Topic: p.Topic, LinkURL: p.LinkURL}
}

// DecodePostPayload parses and normalizes a post payload: platforms are checked, blank parts
// dropped, hashtags cleaned, warnings recomputed.
func DecodePostPayload(raw json.RawMessage) (*PostPayload, error) {
	var p PostPayload
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("post payload: %w", err)
		}
	}
	p.ThreadParts = nonNil(p.ThreadParts)
	p.Hashtags = cleanTags(p.Hashtags)
	p.MediaURLs = nonNil(p.MediaURLs)
	p.Accounts = nonNil(p.Accounts)
	p.Platforms = nonNil(p.Platforms)
	p.Topic, p.LinkURL = strings.TrimSpace(p.Topic), strings.TrimSpace(p.LinkURL)
	if strings.TrimSpace(p.Content) == "" && len(p.ThreadParts) == 0 {
		return nil, errors.New("content is required")
	}
	if strings.TrimSpace(p.Content) == "" {
		p.Content = strings.Join(p.ThreadParts, "\n\n")
	}
	if len(p.Platforms) == 0 {
		return nil, errors.New("at least one platform is required")
	}
	if err := ValidatePlatforms(p.Platforms); err != nil {
		return nil, err
	}
	p.Warnings = p.asPost().ComputeWarnings()
	if p.Warnings == nil {
		p.Warnings = []string{}
	}
	return &p, nil
}

// postKind builds the kind for this module: validation from the platform rules, publishing as
// the action.
func (m *Module) postKind() deliverable.Kind {
	return deliverable.Kind{
		ID: PostKind, Name: "Social post", BuiltIn: true,
		Description: "A post for Threads, Instagram, X... Reviewed like any deliverable; approval publishes it through the publisher connector (the payload's, else the first active one).",
		Schema: map[string]any{"type": "object", "required": []string{"content", "platforms"}, "properties": map[string]any{
			"content":                map[string]any{"type": "string"},
			"thread_parts":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"hashtags":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"platforms":              map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": platformIDs()}},
			"media_urls":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"accounts":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"topic":                  map[string]any{"type": "string"},
			"link_url":               map[string]any{"type": "string"},
			"image_prompt":           map[string]any{"type": "string"},
			"scheduled_at":           map[string]any{"type": "string", "format": "date-time"},
			"publisher_connector_id": map[string]any{"type": "string"},
		}},
		Validate: func(raw json.RawMessage) error {
			_, err := DecodePostPayload(raw)
			return err
		},
		Act: m.publishDeliverable,
	}
}

// publishDeliverable is the post kind's action: hand the post to the publisher under the grant
// the worker attached for this deliverable.
func (m *Module) publishDeliverable(ctx context.Context, d *deliverable.Deliverable) (*connector.ActResult, error) {
	p, err := DecodePostPayload(d.Payload)
	if err != nil {
		return nil, err
	}
	publisherID, err := m.resolvePublisher(ctx, p.PublisherConnectorID)
	if err != nil {
		return nil, err
	}
	pub, c, err := m.Hub.Publisher(ctx, publisherID)
	if err != nil {
		return nil, err
	}
	defer hub.Close(pub)
	urls, files := MediaFor(m.Store, m.MediaBaseURL, p.MediaURLs)
	res, err := pub.Publish(ctx, connector.PublishRequest{
		PostID:      d.ID,
		Title:       d.Title,
		Platforms:   p.Platforms,
		Parts:       p.parts(),
		Hashtags:    p.Hashtags,
		Topic:       p.Topic,
		Link:        p.LinkURL,
		MediaURLs:   urls,
		Media:       files,
		ScheduledAt: p.ScheduledAt,
		Accounts:    p.Accounts,
		Previous:    d.ExternalIDs,
	})
	if err != nil {
		return nil, err
	}
	out := &connector.ActResult{ExternalIDs: map[string]string{}, Details: map[string]any{"publisher": c.Name}}
	if res != nil {
		if res.ExternalIDs != nil {
			out.ExternalIDs = res.ExternalIDs
		}
		for k, v := range res.Details {
			out.Details[k] = v
		}
	}
	return out, nil
}

func (m *Module) resolvePublisher(ctx context.Context, chosen *uuid.UUID) (uuid.UUID, error) {
	if chosen != nil {
		return *chosen, nil
	}
	c, err := m.Store.FirstActiveConnector(ctx, connector.KindPublisher)
	if errors.Is(err, domain.ErrNotFound) {
		return uuid.Nil, errors.New("no active publisher connector configured (add an MCP publisher under Connectors)")
	}
	if err != nil {
		return uuid.Nil, err
	}
	return c.ID, nil
}

// MediaFor resolves media URLs for publishers: absolute URLs, and loaders for the media CueFlow
// stores itself (for publishers whose service cannot fetch CueFlow's URLs).
func MediaFor(st *Store, mediaBase string, raw []string) ([]string, []connector.MediaFile) {
	urls := make([]string, 0, len(raw))
	files := make([]connector.MediaFile, 0, len(raw))
	for _, u := range raw {
		abs := media.Absolute(u, mediaBase)
		f := connector.MediaFile{URL: abs}
		if id, ok := media.IDFromURL(u); ok && strings.HasPrefix(u, media.PathPrefix) {
			f.Stored = true
			f.Load = func(ctx context.Context) ([]byte, string, error) {
				m, err := st.GetMedia(ctx, id)
				if err != nil {
					return nil, "", err
				}
				return m.Data, m.MimeType, nil
			}
		}
		urls = append(urls, abs)
		files = append(files, f)
	}
	return urls, files
}

// nonNil trims a list and drops blanks, never returning nil.
func nonNil(s []string) []string {
	out := make([]string, 0, len(s))
	for _, v := range s {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// cleanTags normalizes hashtags: no leading #, no blanks, no duplicates (case-insensitive).
func cleanTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#"))
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	return out
}
