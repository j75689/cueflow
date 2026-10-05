// Package googleimage generates images with Google's models: Gemini image models
// (models/{m}:generateContent with IMAGE output) and Imagen (models/{m}:predict), through either
// the Gemini API (AI Studio key) or Vertex AI (Application Default Credentials).
package googleimage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/j75689/cueflow/internal/connector"
)

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// Overridable in tests.
var (
	findDefaultCredentials = google.FindDefaultCredentials
	credentialsFromJSON    = google.CredentialsFromJSON //nolint:staticcheck // key supplied by the operator
)

func init() {
	modelHelp := "A Gemini image model (e.g. gemini-2.5-flash-image) or an Imagen model (e.g. imagen-4.0-generate-001)"
	connector.Register(connector.Spec{
		Type:        "gemini_image",
		Kind:        connector.KindImage,
		Name:        "Google AI Studio images",
		Description: "Generates images with Gemini image models or Imagen through the Gemini API, using an AI Studio API key.",
		Fields: []connector.Field{
			{Key: "api_key", Label: "Gemini API key", Type: connector.FieldPassword, Secret: true, Required: true,
				Help: "From aistudio.google.com/apikey"},
			{Key: "model", Label: "Model", Type: connector.FieldString, Default: "gemini-2.5-flash-image", Help: modelHelp},
			{Key: "base_url", Label: "Endpoint override", Type: connector.FieldString,
				Help: "Optional; default https://generativelanguage.googleapis.com/v1beta"},
		},
		DefaultName: func(cfg connector.Config) string {
			return connector.WithDetail("AI Studio images", cfg.String("model"))
		},
	}, newAIStudio)

	connector.Register(connector.Spec{
		Type:        "vertex_image",
		Kind:        connector.KindImage,
		Name:        "Google Vertex AI images",
		Description: "Generates images with Gemini image models or Imagen on Vertex AI. Uses Application Default Credentials unless a service account key is given. Requires a GCP project with billing.",
		Fields: []connector.Field{
			{Key: "project_id", Label: "GCP project ID", Type: connector.FieldString, Required: true},
			{Key: "region", Label: "Region", Type: connector.FieldString, Default: "global",
				Help: "global works for Gemini image models; Imagen needs a region such as us-central1"},
			{Key: "model", Label: "Model", Type: connector.FieldString, Default: "gemini-2.5-flash-image", Help: modelHelp},
			{Key: "service_account_json", Label: "Service account key (JSON)", Type: connector.FieldText, Secret: true,
				Help: "Blank uses Application Default Credentials (gcloud application-default login, GOOGLE_APPLICATION_CREDENTIALS, metadata server)"},
			{Key: "base_url", Label: "Endpoint override", Type: connector.FieldString,
				Help: "Optional; replaces https://{region}-aiplatform.googleapis.com/v1/projects/{project}/locations/{region}"},
		},
		DefaultName: func(cfg connector.Config) string { return connector.WithDetail("Vertex images", cfg.String("model")) },
	}, newVertex)
}

type Generator struct {
	model string
	// url returns the full URL for a model method (generateContent or predict).
	url     func(method string) string
	headers map[string]string
	tokens  oauth2.TokenSource // Vertex only
	http    *http.Client
	label   string
}

func longClient(env connector.Env) *http.Client {
	c := http.DefaultClient
	if env.HTTPClient != nil {
		c = env.HTTPClient
	}
	long := *c
	long.Timeout = 4 * time.Minute
	return &long
}

func newAIStudio(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	model := strings.TrimPrefix(cfg.String("model"), "models/")
	base := strings.TrimRight(cfg.String("base_url"), "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	return &Generator{
		model:   model,
		url:     func(method string) string { return fmt.Sprintf("%s/models/%s:%s", base, model, method) },
		headers: map[string]string{"x-goog-api-key": cfg.String("api_key")},
		http:    longClient(env),
		label:   "AI Studio",
	}, nil
}

func newVertex(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, env.HTTPClient)
	var (
		creds *google.Credentials
		err   error
	)
	if js := cfg.String("service_account_json"); js != "" {
		creds, err = credentialsFromJSON(ctx, []byte(js), cloudPlatformScope)
	} else {
		creds, err = findDefaultCredentials(ctx, cloudPlatformScope)
	}
	if err != nil {
		return nil, fmt.Errorf("vertex: credentials: %w (run `gcloud auth application-default login` or provide a service account key)", err)
	}
	project := cfg.String("project_id")
	region := cfg.String("region")
	if region == "" {
		region = "global"
	}
	model := strings.TrimPrefix(cfg.String("model"), "google/")
	base := strings.TrimRight(cfg.String("base_url"), "/")
	if base == "" {
		host := "aiplatform.googleapis.com"
		if region != "global" {
			host = region + "-aiplatform.googleapis.com"
		}
		base = fmt.Sprintf("https://%s/v1/projects/%s/locations/%s", host, project, region)
	}
	g := &Generator{
		model: model,
		url: func(method string) string {
			return fmt.Sprintf("%s/publishers/google/models/%s:%s", base, model, method)
		},
		headers: map[string]string{},
		tokens:  oauth2.ReuseTokenSource(nil, creds.TokenSource),
		http:    longClient(env),
		label:   "Vertex",
	}
	// gcloud user credentials have no project of their own; Vertex needs the quota project named.
	var meta struct {
		Type string `json:"type"`
	}
	if len(creds.JSON) > 0 && json.Unmarshal(creds.JSON, &meta) == nil && meta.Type == "authorized_user" {
		g.headers["x-goog-user-project"] = project
	}
	return g, nil
}

func (g *Generator) post(ctx context.Context, method string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url(method), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range g.headers {
		req.Header.Set(k, v)
	}
	if g.tokens != nil {
		tok, err := g.tokens.Token()
		if err != nil {
			return nil, fmt.Errorf("vertex: token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
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
			return nil, fmt.Errorf("%s images: HTTP %d: %s", g.label, resp.StatusCode, e.Error.Message)
		}
		return nil, fmt.Errorf("%s images: HTTP %d: %.300s", g.label, resp.StatusCode, data)
	}
	return data, nil
}

func (g *Generator) imagen() bool { return strings.HasPrefix(g.model, "imagen") }

// Test makes no image (that would cost money): it checks credentials with a request that fails
// validation for free when the key and model are good.
func (g *Generator) Test(ctx context.Context) (*connector.TestResult, error) {
	if g.model == "" {
		return &connector.TestResult{OK: false, Message: "no model configured"}, nil
	}
	var err error
	if g.imagen() {
		_, err = g.post(ctx, "predict", map[string]any{"instances": []any{}})
	} else {
		_, err = g.post(ctx, "countTokens", map[string]any{"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "test"}}}}})
	}
	if err == nil || strings.Contains(err.Error(), "HTTP 400") {
		return &connector.TestResult{OK: true, Message: fmt.Sprintf("%s reachable; model %s", g.label, g.model)}, nil
	}
	return &connector.TestResult{OK: false, Message: err.Error()}, nil
}

func (g *Generator) GenerateImage(ctx context.Context, req connector.ImageRequest) ([]connector.Image, error) {
	n := req.N
	if n < 1 {
		n = 1
	}
	if g.imagen() {
		return g.predict(ctx, req.Prompt, imagenAspect(req.Aspect), n)
	}
	var out []connector.Image
	for range n { // Gemini image models return one image per call
		imgs, err := g.generateContent(ctx, req.Prompt, req.Aspect)
		if err != nil {
			return nil, err
		}
		out = append(out, imgs...)
	}
	return out, nil
}

// imagenAspect maps to Imagen's supported ratios (1:1, 3:4, 4:3, 9:16, 16:9).
func imagenAspect(a string) string {
	if a == "4:5" {
		return "3:4"
	}
	if a == "" {
		return "1:1"
	}
	return a
}

func (g *Generator) predict(ctx context.Context, prompt, aspect string, n int) ([]connector.Image, error) {
	data, err := g.post(ctx, "predict", map[string]any{
		"instances":  []any{map[string]any{"prompt": prompt}},
		"parameters": map[string]any{"sampleCount": n, "aspectRatio": aspect},
	})
	if err != nil {
		return nil, err
	}
	var out struct {
		Predictions []struct {
			Bytes          string `json:"bytesBase64Encoded"`
			MimeType       string `json:"mimeType"`
			RaiFilteredFor string `json:"raiFilteredReason"`
		} `json:"predictions"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s images: unexpected reply: %.300s", g.label, data)
	}
	var images []connector.Image
	var filtered []string
	for _, p := range out.Predictions {
		if p.Bytes == "" {
			if p.RaiFilteredFor != "" {
				filtered = append(filtered, p.RaiFilteredFor)
			}
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(p.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s images: bad base64: %w", g.label, err)
		}
		images = append(images, connector.Image{Data: raw, MimeType: p.MimeType})
	}
	if len(images) == 0 {
		if len(filtered) > 0 {
			return nil, fmt.Errorf("%s images: blocked by safety filters: %s", g.label, strings.Join(filtered, "; "))
		}
		return nil, fmt.Errorf("%s images: no image returned (the prompt may have been filtered)", g.label)
	}
	return images, nil
}

type inline struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

func (g *Generator) generateContent(ctx context.Context, prompt, aspect string) ([]connector.Image, error) {
	gen := map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}}
	if aspect != "" {
		gen["imageConfig"] = map[string]any{"aspectRatio": aspect}
	}
	data, err := g.post(ctx, "generateContent", map[string]any{
		"contents":         []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": prompt}}}},
		"generationConfig": gen,
	})
	if err != nil {
		return nil, err
	}
	var out struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text       string  `json:"text"`
					InlineData *inline `json:"inlineData"`
					Inline2    *inline `json:"inline_data"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s images: unexpected reply: %.300s", g.label, data)
	}
	var images []connector.Image
	var text, finish []string
	for _, c := range out.Candidates {
		if c.FinishReason != "" && c.FinishReason != "STOP" {
			finish = append(finish, c.FinishReason)
		}
		for _, p := range c.Content.Parts {
			in := p.InlineData
			if in == nil {
				in = p.Inline2
			}
			if in == nil || in.Data == "" {
				if p.Text != "" {
					text = append(text, p.Text)
				}
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(in.Data)
			if err != nil {
				return nil, fmt.Errorf("%s images: bad base64: %w", g.label, err)
			}
			images = append(images, connector.Image{Data: raw, MimeType: in.MimeType})
		}
	}
	if len(images) == 0 {
		switch {
		case out.PromptFeedback.BlockReason != "":
			return nil, fmt.Errorf("%s images: prompt blocked (%s)", g.label, out.PromptFeedback.BlockReason)
		case len(finish) > 0:
			return nil, fmt.Errorf("%s images: no image (finish reason %s)", g.label, strings.Join(finish, ", "))
		case len(text) > 0:
			return nil, fmt.Errorf("%s images: the model answered with text instead of an image: %.200s", g.label, strings.Join(text, " "))
		}
		return nil, fmt.Errorf("%s images: no image returned", g.label)
	}
	return images, nil
}
