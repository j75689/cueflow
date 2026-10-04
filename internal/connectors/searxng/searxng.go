// Package searxng is the search connector for a self-hosted SearXNG instance.
package searxng

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/j75689/cueflow/internal/connector"
)

func init() {
	connector.Register(connector.Spec{
		Type:        "searxng",
		Kind:        connector.KindSearch,
		Name:        "SearXNG",
		Description: "Self-hosted metasearch. The instance must have the JSON output format enabled (search.formats: [html, json]).",
		Fields: []connector.Field{
			{Key: "endpoint_url", Label: "Instance URL", Type: connector.FieldString, Required: true, Placeholder: "http://searxng:8080"},
			{Key: "language", Label: "Language", Type: connector.FieldString, Default: "auto"},
			{Key: "time_range", Label: "Time range", Type: connector.FieldSelect, Options: []string{"", "day", "week", "month", "year"}, Default: "week"},
			{Key: "categories", Label: "Categories", Type: connector.FieldString, Default: "general,news"},
		},
	}, New)
}

type Searcher struct {
	base, lang, timeRange, categories string
	http                              *http.Client
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	return &Searcher{
		base:       strings.TrimRight(cfg.String("endpoint_url"), "/"),
		lang:       cfg.String("language"),
		timeRange:  cfg.String("time_range"),
		categories: cfg.String("categories"),
		http:       env.HTTPClient,
	}, nil
}

func (s *Searcher) Test(ctx context.Context) (*connector.TestResult, error) {
	res, err := s.Search(ctx, "open source", 3)
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	return &connector.TestResult{OK: true, Message: fmt.Sprintf("search ok, %d results", len(res))}, nil
}

func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]connector.SearchResult, error) {
	q := url.Values{"q": {query}, "format": {"json"}}
	if s.lang != "" {
		q.Set("language", s.lang)
	}
	if s.timeRange != "" {
		q.Set("time_range", s.timeRange)
	}
	if s.categories != "" {
		q.Set("categories", s.categories)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("searxng: HTTP %d (is the json format enabled?)", resp.StatusCode)
	}
	var body struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			PublishedDate string `json:"publishedDate"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("searxng: decode: %w", err)
	}
	out := make([]connector.SearchResult, 0, limit)
	for _, r := range body.Results {
		out = append(out, connector.SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content, Date: r.PublishedDate})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
