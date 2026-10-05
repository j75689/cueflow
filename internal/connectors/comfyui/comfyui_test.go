package comfyui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/j75689/cueflow/internal/connector"
)

var png = []byte("\x89PNG\r\n\x1a\nfake")

func TestGenerate(t *testing.T) {
	var queued map[string]any
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt":
			_ = json.NewDecoder(r.Body).Decode(&queued)
			_, _ = w.Write([]byte(`{"prompt_id":"p1","number":1,"node_errors":{}}`))
		case r.URL.Path == "/history/p1":
			if polls.Add(1) < 2 {
				_, _ = w.Write([]byte(`{}`)) // still running
				return
			}
			_, _ = w.Write([]byte(`{"p1":{"status":{"status_str":"success","completed":true},"outputs":{"9":{"images":[
				{"filename":"cueflow_0001.png","subfolder":"","type":"output"},{"filename":"tmp.png","subfolder":"","type":"temp"}]}}}}`))
		case r.URL.Path == "/view" && r.URL.Query().Get("filename") == "cueflow_0001.png":
			_, _ = w.Write(png)
		default:
			http.Error(w, "unexpected "+r.URL.String(), 404)
		}
	}))
	defer srv.Close()
	inst, err := New(connector.Config{"base_url": srv.URL, "checkpoint": "flux.safetensors", "negative_prompt": "blurry", "steps": 20},
		connector.Env{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	g := inst.(*Generator)
	g.poll = 10 * time.Millisecond
	imgs, err := g.GenerateImage(context.Background(), connector.ImageRequest{Prompt: `say "hi"`, Aspect: "9:16"})
	if err != nil || len(imgs) != 1 || string(imgs[0].Data) != string(png) {
		t.Fatalf("images: %+v %v", imgs, err)
	}
	wf := queued["prompt"].(map[string]any)
	latent := wf["5"].(map[string]any)["inputs"].(map[string]any)
	if latent["width"] != float64(768) || latent["height"] != float64(1344) {
		t.Fatalf("size: %v", latent)
	}
	if wf["6"].(map[string]any)["inputs"].(map[string]any)["text"] != `say "hi"` ||
		wf["4"].(map[string]any)["inputs"].(map[string]any)["ckpt_name"] != "flux.safetensors" ||
		wf["3"].(map[string]any)["inputs"].(map[string]any)["steps"] != float64(20) {
		t.Fatalf("placeholders: %v", wf)
	}
}

func TestWorkflowError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"type":"prompt_outputs_failed_validation","message":"Value not in list: ckpt_name"},"node_errors":{}}`))
	}))
	defer srv.Close()
	inst, _ := New(connector.Config{"base_url": srv.URL}, connector.Env{HTTPClient: srv.Client()})
	_, err := inst.(*Generator).GenerateImage(context.Background(), connector.ImageRequest{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "ckpt_name") {
		t.Fatalf("err: %v", err)
	}
}

func TestBadWorkflowJSON(t *testing.T) {
	if _, err := New(connector.Config{"base_url": "http://x", "workflow": "{nope"}, connector.Env{}); err == nil {
		t.Fatal("invalid workflow JSON should be refused")
	}
}
