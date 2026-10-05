// Package openaiimage generates images through the OpenAI Images API (POST {base}/images/generations)
// or any service that speaks it: OpenAI, Azure OpenAI, or a self-hosted compatible server.
package openaiimage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/j75689/cueflow/internal/connector"
)

func init() {
	connector.Register(connector.Spec{
		Type:        "openai_image",
		Kind:        connector.KindImage,
		Name:        "OpenAI-compatible images",
		Description: "Generates images through the OpenAI Images API (gpt-image models), Azure OpenAI, or a compatible server.",
		Fields: []connector.Field{
			{Key: "base_url", Label: "Base URL", Type: connector.FieldString, Default: "https://api.openai.com/v1",
				Help: "Ends before /images/generations. Azure: https://<resource>.openai.azure.com/openai/deployments/<deployment>"},
			{Key: "api_key", Label: "API key", Type: connector.FieldPassword, Secret: true},
			{Key: "model", Label: "Model", Type: connector.FieldString, Default: "gpt-image-1",
				Help: "e.g. gpt-image-1 or dall-e-3 (Azure: ignored by most deployments)"},
			{Key: "quality", Label: "Quality", Type: connector.FieldSelect, Options: []string{"auto", "low", "medium", "high"}, Default: "auto",
				Help: "Higher quality costs more and takes longer"},
			{Key: "auth_header", Label: "Auth header", Type: connector.FieldSelect, Options: []string{"bearer", "api-key"}, Default: "bearer",
				Help: "api-key for Azure OpenAI"},
			{Key: "api_version", Label: "API version", Type: connector.FieldString, Help: "Azure only, e.g. 2025-04-01-preview"},
			{Key: "headers", Label: "Extra headers", Type: connector.FieldKV},
		},
		DefaultName: func(cfg connector.Config) string {
			return connector.WithDetail("OpenAI images", cfg.String("model"))
		},
	}, New)
}

type Generator struct {
	cfg  connector.Config
	http *http.Client
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	if strings.TrimSpace(cfg.String("base_url")) == "" {
		return nil, fmt.Errorf("openai_image: base URL is required")
	}
	c := http.DefaultClient
	if env.HTTPClient != nil {
		c = env.HTTPClient
	}
	long := *c
	long.Timeout = 4 * time.Minute // high-quality images can take a minute or more
	return &Generator{cfg: cfg, http: &long}, nil
}

func (g *Generator) endpoint(path string) string {
	u := strings.TrimRight(g.cfg.String("base_url"), "/") + path
	if v := g.cfg.String("api_version"); v != "" {
		u += "?api-version=" + url.QueryEscape(v)
	}
	return u
}

func (g *Generator) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.endpoint(path), rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if k := g.cfg.String("api_key"); k != "" {
		if g.cfg.String("auth_header") == "api-key" {
			req.Header.Set("api-key", k)
		} else {
			req.Header.Set("Authorization", "Bearer "+k)
		}
	}
	for k, v := range g.cfg.KV("headers") {
		req.Header.Set(k, v)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("openai images: HTTP %d: %s", resp.StatusCode, e.Error.Message)
		}
		return nil, fmt.Errorf("openai images: HTTP %d: %.300s", resp.StatusCode, data)
	}
	return data, nil
}

// Test checks the key by listing models; servers without /models count as reachable.
func (g *Generator) Test(ctx context.Context) (*connector.TestResult, error) {
	if _, err := g.do(ctx, http.MethodGet, "/models", nil); err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return &connector.TestResult{OK: true, Message: "reachable (no /models endpoint; generate an image to fully check)"}, nil
		}
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	return &connector.TestResult{OK: true, Message: "connected; model " + g.cfg.String("model")}, nil
}

// size maps an aspect ratio to the closest size the model supports.
func size(model, aspect string) string {
	portrait, landscape := "1024x1536", "1536x1024"
	if strings.HasPrefix(model, "dall-e-3") {
		portrait, landscape = "1024x1792", "1792x1024"
	}
	switch aspect {
	case "4:5", "3:4", "9:16":
		return portrait
	case "16:9", "4:3":
		return landscape
	}
	return "1024x1024"
}

func (g *Generator) GenerateImage(ctx context.Context, req connector.ImageRequest) ([]connector.Image, error) {
	model := g.cfg.String("model")
	n := req.N
	if n < 1 {
		n = 1
	}
	body := map[string]any{"prompt": req.Prompt, "n": n, "size": size(model, req.Aspect)}
	if model != "" {
		body["model"] = model
	}
	if q := g.cfg.String("quality"); q != "" && q != "auto" {
		if strings.HasPrefix(model, "dall-e-3") {
			q = map[bool]string{true: "hd", false: "standard"}[q == "high"]
		}
		body["quality"] = q
	}
	if strings.HasPrefix(model, "dall-e") {
		body["response_format"] = "b64_json" // gpt-image models always return base64 and reject this field
	}
	data, err := g.do(ctx, http.MethodPost, "/images/generations", body)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("openai images: unexpected reply: %.300s", data)
	}
	var images []connector.Image
	for _, d := range out.Data {
		var raw []byte
		switch {
		case d.B64JSON != "":
			if raw, err = base64.StdEncoding.DecodeString(d.B64JSON); err != nil {
				return nil, fmt.Errorf("openai images: bad base64: %w", err)
			}
		case d.URL != "":
			if raw, err = g.download(ctx, d.URL); err != nil {
				return nil, err
			}
		default:
			continue
		}
		images = append(images, connector.Image{Data: raw, MimeType: http.DetectContentType(raw), RevisedPrompt: d.RevisedPrompt})
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("openai images: no image in reply")
	}
	return images, nil
}

// download fetches an image the provider returned by URL (only https, from the provider's CDN).
func (g *Generator) download(ctx context.Context, u string) ([]byte, error) {
	if !strings.HasPrefix(u, "https://") {
		return nil, fmt.Errorf("openai images: refusing non-https image URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openai images: download HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}
