// Package tavily is the search connector for the Tavily API.
package tavily

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/j75689/cueflow/internal/connector"
)

func init() {
	connector.Register(connector.Spec{
		Type:        "tavily",
		Kind:        connector.KindSearch,
		Name:        "Tavily",
		Description: "Hosted web search API built for LLM agents.",
		Fields: []connector.Field{
			{Key: "api_key", Label: "API key", Type: connector.FieldPassword, Required: true, Secret: true},
			{Key: "topic", Label: "Topic", Type: connector.FieldSelect, Options: []string{"general", "news"}, Default: "news"},
			{Key: "days", Label: "Days back (news topic)", Type: connector.FieldNumber, Default: 3},
			{Key: "endpoint_url", Label: "API base URL", Type: connector.FieldString, Default: "https://api.tavily.com"},
		},
	}, New)
}

type Searcher struct {
	key, topic, base string
	days             int
	http             *http.Client
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	return &Searcher{
		key:   cfg.String("api_key"),
		topic: cfg.String("topic"),
		days:  cfg.Int("days", 3),
		base:  strings.TrimRight(cfg.String("endpoint_url"), "/"),
		http:  env.HTTPClient,
	}, nil
}

func (s *Searcher) Test(ctx context.Context) (*connector.TestResult, error) {
	res, err := s.Search(ctx, "technology news", 2)
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	return &connector.TestResult{OK: true, Message: fmt.Sprintf("search ok, %d results", len(res))}, nil
}

func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]connector.SearchResult, error) {
	if limit <= 0 {
		limit = 5
	}
	payload := map[string]any{"query": query, "max_results": limit, "topic": s.topic}
	if s.topic == "news" && s.days > 0 {
		payload["days"] = s.days
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/search", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.key)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("tavily: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var body struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			PublishedDate string `json:"published_date"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("tavily: decode: %w", err)
	}
	out := make([]connector.SearchResult, 0, len(body.Results))
	for _, r := range body.Results {
		out = append(out, connector.SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content, Date: r.PublishedDate})
	}
	return out, nil
}
