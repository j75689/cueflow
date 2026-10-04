// Package rss is the RSS/Atom source connector.
package rss

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/j75689/cueflow/internal/connector"
)

func init() {
	connector.Register(connector.Spec{
		Type:        "rss",
		Kind:        connector.KindSource,
		Name:        "RSS / Atom feed",
		Description: "Incrementally pulls entries from an RSS or Atom feed (deduplicated by GUID).",
		Fields: []connector.Field{
			{Key: "url", Label: "Feed URL", Type: connector.FieldString, Required: true, Placeholder: "https://hnrss.org/frontpage"},
			{Key: "max_content_chars", Label: "Max content characters per entry", Type: connector.FieldNumber, Default: 4000},
		},
	}, New)
}

type Source struct {
	url      string
	maxChars int
	parser   *gofeed.Parser
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	p := gofeed.NewParser()
	p.Client = env.HTTPClient
	p.UserAgent = "CueFlow/0.1 (+https://github.com/j75689/cueflow)"
	return &Source{url: cfg.String("url"), maxChars: cfg.Int("max_content_chars", 4000), parser: p}, nil
}

func (s *Source) Test(ctx context.Context) (*connector.TestResult, error) {
	feed, err := s.parser.ParseURLWithContext(s.url, ctx)
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	return &connector.TestResult{OK: true, Message: fmt.Sprintf("%q: %d entries", feed.Title, len(feed.Items)),
		Details: map[string]any{"title": feed.Title, "entries": len(feed.Items)}}, nil
}

// Fetch returns entries published (or, when undated, seen) after opts.Since. Deduplication against
// previously stored entries happens in the store via (connector_id, external_id).
func (s *Source) Fetch(ctx context.Context, opts connector.FetchOptions) ([]connector.Item, error) {
	feed, err := s.parser.ParseURLWithContext(s.url, ctx)
	if err != nil {
		return nil, fmt.Errorf("rss: %w", err)
	}
	items := make([]connector.Item, 0, len(feed.Items))
	for _, e := range feed.Items {
		pub := e.PublishedParsed
		if pub == nil {
			pub = e.UpdatedParsed
		}
		if pub != nil && !opts.Since.IsZero() && pub.Before(opts.Since) {
			continue
		}
		content := e.Content
		if content == "" {
			content = e.Description
		}
		author := ""
		if e.Author != nil {
			author = e.Author.Name
		}
		items = append(items, connector.Item{
			ExternalID:  ExternalID(e),
			Title:       strings.TrimSpace(e.Title),
			URL:         e.Link,
			Author:      author,
			Content:     truncate(StripHTML(content), s.maxChars),
			PublishedAt: utc(pub),
		})
		if opts.Limit > 0 && len(items) >= opts.Limit {
			break
		}
	}
	return items, nil
}

// ExternalID is the entry's GUID, falling back to its link, then a content hash.
func ExternalID(e *gofeed.Item) string {
	if e.GUID != "" {
		return e.GUID
	}
	if e.Link != "" {
		return e.Link
	}
	sum := sha256.Sum256([]byte(e.Title + "\x00" + e.Description))
	return "sha256:" + hex.EncodeToString(sum[:16])
}

var (
	tagRe   = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>|<[^>]+>`)
	spaceRe = regexp.MustCompile(`[ \t]+`)
	nlRe    = regexp.MustCompile(`\n{3,}`)
)

// StripHTML converts an HTML fragment to readable plain text.
func StripHTML(s string) string {
	s = strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n", "</p>", "\n\n", "</li>", "\n").Replace(s)
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = spaceRe.ReplaceAllString(s, " ")
	s = nlRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if n <= 0 || len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
