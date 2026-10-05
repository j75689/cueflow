// Package comfyui generates images with a self-hosted ComfyUI server: it queues a workflow in API
// format (POST /prompt), polls /history/{id}, and downloads the outputs (/view).
//
// The workflow may use these placeholders: "{{prompt}}", "{{negative_prompt}}", "{{checkpoint}}"
// inside strings, and "{{width}}", "{{height}}", "{{seed}}", "{{steps}}" as whole (quoted) values,
// which are replaced by numbers.
package comfyui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
)

// DefaultWorkflow is a plain text-to-image graph for a single checkpoint (SDXL or SD 1.5).
const DefaultWorkflow = `{
  "4": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": "{{checkpoint}}"}},
  "5": {"class_type": "EmptyLatentImage", "inputs": {"width": "{{width}}", "height": "{{height}}", "batch_size": 1}},
  "6": {"class_type": "CLIPTextEncode", "inputs": {"text": "{{prompt}}", "clip": ["4", 1]}},
  "7": {"class_type": "CLIPTextEncode", "inputs": {"text": "{{negative_prompt}}", "clip": ["4", 1]}},
  "3": {"class_type": "KSampler", "inputs": {"seed": "{{seed}}", "steps": "{{steps}}", "cfg": 7, "sampler_name": "euler",
        "scheduler": "normal", "denoise": 1, "model": ["4", 0], "positive": ["6", 0], "negative": ["7", 0], "latent_image": ["5", 0]}},
  "8": {"class_type": "VAEDecode", "inputs": {"samples": ["3", 0], "vae": ["4", 2]}},
  "9": {"class_type": "SaveImage", "inputs": {"filename_prefix": "cueflow", "images": ["8", 0]}}
}`

func init() {
	connector.Register(connector.Spec{
		Type:        "comfyui",
		Kind:        connector.KindImage,
		Name:        "ComfyUI",
		Description: "Generates images on your own ComfyUI server (Stable Diffusion, Flux...). No API costs; needs a GPU.",
		Fields: []connector.Field{
			{Key: "base_url", Label: "ComfyUI URL", Type: connector.FieldString, Required: true, Placeholder: "http://comfyui:8188"},
			{Key: "checkpoint", Label: "Checkpoint", Type: connector.FieldString, Default: "sd_xl_base_1.0.safetensors",
				Help: "File name under models/checkpoints, used by the default workflow"},
			{Key: "negative_prompt", Label: "Negative prompt", Type: connector.FieldString, Default: "text, watermark, logo, blurry, low quality"},
			{Key: "steps", Label: "Steps", Type: connector.FieldNumber, Default: 25},
			{Key: "workflow", Label: "Workflow (API format JSON)", Type: connector.FieldText,
				Help: "Optional. Export with \"Save (API Format)\" and use {{prompt}}, {{negative_prompt}}, {{width}}, {{height}}, {{seed}}, {{steps}}, {{checkpoint}}. Blank uses a basic checkpoint workflow"},
			{Key: "timeout_minutes", Label: "Timeout (minutes)", Type: connector.FieldNumber, Default: 5},
			{Key: "headers", Label: "Extra headers", Type: connector.FieldKV, Help: "e.g. Authorization for a reverse proxy"},
		},
		DefaultName: func(cfg connector.Config) string {
			if u, err := url.Parse(cfg.String("base_url")); err == nil && u.Host != "" {
				return connector.WithDetail("ComfyUI", u.Host)
			}
			return ""
		},
	}, New)
}

type Generator struct {
	cfg  connector.Config
	base string
	http *http.Client
	poll time.Duration
}

func New(cfg connector.Config, env connector.Env) (connector.Instance, error) {
	base := strings.TrimRight(cfg.String("base_url"), "/")
	if base == "" {
		return nil, errors.New("comfyui: URL is required")
	}
	if wf := strings.TrimSpace(cfg.String("workflow")); wf != "" && !json.Valid([]byte(wf)) {
		return nil, errors.New("comfyui: workflow is not valid JSON")
	}
	c := http.DefaultClient
	if env.HTTPClient != nil {
		c = env.HTTPClient
	}
	return &Generator{cfg: cfg, base: base, http: c, poll: time.Second}, nil
}

func (g *Generator) request(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, rd)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range g.cfg.KV("headers") {
		req.Header.Set(k, v)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return data, resp.StatusCode, err
}

func (g *Generator) Test(ctx context.Context) (*connector.TestResult, error) {
	data, code, err := g.request(ctx, http.MethodGet, "/system_stats", nil)
	if err != nil {
		return &connector.TestResult{OK: false, Message: err.Error()}, nil
	}
	if code != http.StatusOK {
		return &connector.TestResult{OK: false, Message: fmt.Sprintf("HTTP %d from /system_stats", code)}, nil
	}
	var stats struct {
		System struct {
			Version string `json:"comfyui_version"`
		} `json:"system"`
		Devices []struct {
			Name string `json:"name"`
		} `json:"devices"`
	}
	_ = json.Unmarshal(data, &stats)
	msg := "connected to ComfyUI"
	if stats.System.Version != "" {
		msg += " " + stats.System.Version
	}
	if len(stats.Devices) > 0 {
		msg += " on " + stats.Devices[0].Name
	}
	return &connector.TestResult{OK: true, Message: msg}, nil
}

// dims maps an aspect ratio to SDXL-friendly sizes (multiples of 64, about one megapixel).
func dims(aspect string) (int, int) {
	switch aspect {
	case "4:5":
		return 896, 1088
	case "3:4":
		return 896, 1152
	case "9:16":
		return 768, 1344
	case "16:9":
		return 1344, 768
	case "4:3":
		return 1152, 896
	}
	return 1024, 1024
}

// workflow fills the placeholders and returns the graph to queue.
func (g *Generator) workflow(prompt, aspect string, seed uint64) (json.RawMessage, error) {
	wf := strings.TrimSpace(g.cfg.String("workflow"))
	if wf == "" {
		wf = DefaultWorkflow
	}
	w, h := dims(aspect)
	str := func(s string) string { b, _ := json.Marshal(s); return string(b[1 : len(b)-1]) }
	r := strings.NewReplacer(
		`"{{width}}"`, strconv.Itoa(w), `"{{height}}"`, strconv.Itoa(h),
		`"{{seed}}"`, strconv.FormatUint(seed, 10), `"{{steps}}"`, strconv.Itoa(g.cfg.Int("steps", 25)),
		"{{prompt}}", str(prompt), "{{negative_prompt}}", str(g.cfg.String("negative_prompt")),
		"{{checkpoint}}", str(g.cfg.String("checkpoint")),
	)
	out := r.Replace(wf)
	if !json.Valid([]byte(out)) {
		return nil, errors.New("comfyui: workflow is not valid JSON after filling placeholders")
	}
	return json.RawMessage(out), nil
}

func randomSeed() uint64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint64(b[:]) >> 14 // ComfyUI seeds fit in 50 bits comfortably
}

func (g *Generator) GenerateImage(ctx context.Context, req connector.ImageRequest) ([]connector.Image, error) {
	n := req.N
	if n < 1 {
		n = 1
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(g.cfg.Int("timeout_minutes", 5))*time.Minute)
	defer cancel()
	var out []connector.Image
	for range n {
		imgs, err := g.generateOne(ctx, req.Prompt, req.Aspect)
		if err != nil {
			return nil, err
		}
		out = append(out, imgs...)
	}
	return out, nil
}

type imageRef struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

func (g *Generator) generateOne(ctx context.Context, prompt, aspect string) ([]connector.Image, error) {
	wf, err := g.workflow(prompt, aspect, randomSeed())
	if err != nil {
		return nil, err
	}
	data, code, err := g.request(ctx, http.MethodPost, "/prompt", map[string]any{"prompt": wf, "client_id": uuid.NewString()})
	if err != nil {
		return nil, fmt.Errorf("comfyui: queue: %w", err)
	}
	var queued struct {
		PromptID string          `json:"prompt_id"`
		Error    json.RawMessage `json:"error"`
		Nodes    json.RawMessage `json:"node_errors"`
	}
	_ = json.Unmarshal(data, &queued)
	if code != http.StatusOK || queued.PromptID == "" {
		return nil, fmt.Errorf("comfyui: workflow rejected (HTTP %d): %.400s", code, data)
	}

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("comfyui: timed out waiting for the image: %w", ctx.Err())
		case <-time.After(g.poll):
		}
		data, code, err := g.request(ctx, http.MethodGet, "/history/"+url.PathEscape(queued.PromptID), nil)
		if err != nil || code != http.StatusOK {
			continue // transient; keep polling until the timeout
		}
		var hist map[string]struct {
			Status struct {
				StatusStr string `json:"status_str"`
				Completed bool   `json:"completed"`
				Messages  []any  `json:"messages"`
			} `json:"status"`
			Outputs map[string]struct {
				Images []imageRef `json:"images"`
			} `json:"outputs"`
		}
		if json.Unmarshal(data, &hist) != nil {
			continue
		}
		entry, ok := hist[queued.PromptID]
		if !ok {
			continue // still queued or running
		}
		if entry.Status.StatusStr == "error" {
			msg, _ := json.Marshal(entry.Status.Messages)
			return nil, fmt.Errorf("comfyui: workflow failed: %.400s", msg)
		}
		if !entry.Status.Completed && entry.Status.StatusStr != "success" {
			continue
		}
		var images []connector.Image
		for _, o := range entry.Outputs {
			for _, ref := range o.Images {
				if ref.Type != "" && ref.Type != "output" {
					continue // skip previews
				}
				raw, err := g.view(ctx, ref)
				if err != nil {
					return nil, err
				}
				images = append(images, connector.Image{Data: raw, MimeType: http.DetectContentType(raw)})
			}
		}
		if len(images) == 0 {
			return nil, errors.New("comfyui: the workflow finished without saving an image (add a SaveImage node)")
		}
		return images, nil
	}
}

func (g *Generator) view(ctx context.Context, ref imageRef) ([]byte, error) {
	q := url.Values{"filename": {ref.Filename}, "subfolder": {ref.Subfolder}, "type": {ref.Type}}
	data, code, err := g.request(ctx, http.MethodGet, "/view?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("comfyui: download: %w", err)
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("comfyui: download HTTP %d", code)
	}
	return data, nil
}
