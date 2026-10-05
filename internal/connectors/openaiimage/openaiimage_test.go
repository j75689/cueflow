package openaiimage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/connector"
)

var png = []byte("\x89PNG\r\n\x1a\nfake")

func TestGenerate(t *testing.T) {
	var got map[string]any
	var header http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		if r.URL.Path != "/v1/images/generations" || r.URL.Query().Get("api-version") != "2025-04-01-preview" {
			http.Error(w, "bad path "+r.URL.String(), 404)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"b64_json": base64.StdEncoding.EncodeToString(png)}}})
	}))
	defer srv.Close()
	inst, err := New(connector.Config{"base_url": srv.URL + "/v1", "api_key": "k", "model": "gpt-image-1", "quality": "high",
		"auth_header": "api-key", "api_version": "2025-04-01-preview"}, connector.Env{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := inst.(*Generator).GenerateImage(context.Background(), connector.ImageRequest{Prompt: "a cat", Aspect: "4:5", N: 1})
	if err != nil || len(imgs) != 1 || string(imgs[0].Data) != string(png) || imgs[0].MimeType != "image/png" {
		t.Fatalf("images: %+v %v", imgs, err)
	}
	if got["size"] != "1024x1536" || got["quality"] != "high" || got["model"] != "gpt-image-1" || got["response_format"] != nil {
		t.Fatalf("request: %v", got)
	}
	if header.Get("api-key") != "k" || header.Get("Authorization") != "" {
		t.Fatalf("auth header: %v", header)
	}
}

func TestErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"Your request was rejected by the safety system."}}`))
	}))
	defer srv.Close()
	inst, _ := New(connector.Config{"base_url": srv.URL, "model": "dall-e-3"}, connector.Env{HTTPClient: srv.Client()})
	_, err := inst.(*Generator).GenerateImage(context.Background(), connector.ImageRequest{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "safety system") {
		t.Fatalf("err: %v", err)
	}
}

func TestSizes(t *testing.T) {
	cases := map[[2]string]string{{"gpt-image-1", "1:1"}: "1024x1024", {"gpt-image-1", "16:9"}: "1536x1024", {"dall-e-3", "9:16"}: "1024x1792"}
	for in, want := range cases {
		if got := size(in[0], in[1]); got != want {
			t.Errorf("%v: %s, want %s", in, got, want)
		}
	}
}
