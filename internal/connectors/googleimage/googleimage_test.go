package googleimage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/j75689/cueflow/internal/connector"
)

var png = []byte("\x89PNG\r\n\x1a\nfake")

func TestAIStudioGemini(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-2.5-flash-image:generateContent" || r.Header.Get("x-goog-api-key") != "key" {
			http.Error(w, "bad request "+r.URL.Path, 404)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
			map[string]any{"text": "Here you go"},
			map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(png)}},
		}}}}})
	}))
	defer srv.Close()
	inst, _ := newAIStudio(connector.Config{"api_key": "key", "model": "gemini-2.5-flash-image", "base_url": srv.URL + "/v1beta"},
		connector.Env{HTTPClient: srv.Client()})
	imgs, err := inst.(*Generator).GenerateImage(context.Background(), connector.ImageRequest{Prompt: "a fox", Aspect: "4:5"})
	if err != nil || len(imgs) != 1 || string(imgs[0].Data) != string(png) {
		t.Fatalf("images: %+v %v", imgs, err)
	}
	gen := got["generationConfig"].(map[string]any)
	if gen["imageConfig"].(map[string]any)["aspectRatio"] != "4:5" {
		t.Fatalf("request: %v", got)
	}
}

func TestGeminiTextOnlyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"I can't draw that."}]}}]}`))
	}))
	defer srv.Close()
	inst, _ := newAIStudio(connector.Config{"api_key": "key", "model": "gemini-x", "base_url": srv.URL}, connector.Env{HTTPClient: srv.Client()})
	_, err := inst.(*Generator).GenerateImage(context.Background(), connector.ImageRequest{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "can't draw") {
		t.Fatalf("err: %v", err)
	}
}

func TestVertexImagen(t *testing.T) {
	findDefaultCredentials = func(context.Context, ...string) (*google.Credentials, error) {
		return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok"}),
			JSON: []byte(`{"type":"authorized_user"}`)}, nil
	}
	t.Cleanup(func() { findDefaultCredentials = google.FindDefaultCredentials })
	var got map[string]any
	var header http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		if r.URL.Path != "/publishers/google/models/imagen-4.0-generate-001:predict" {
			http.Error(w, "bad path "+r.URL.Path, 404)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"predictions": []any{
			map[string]any{"bytesBase64Encoded": base64.StdEncoding.EncodeToString(png), "mimeType": "image/png"}}})
	}))
	defer srv.Close()
	inst, err := newVertex(connector.Config{"project_id": "p1", "region": "us-central1", "model": "imagen-4.0-generate-001", "base_url": srv.URL},
		connector.Env{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := inst.(*Generator).GenerateImage(context.Background(), connector.ImageRequest{Prompt: "a fox", Aspect: "4:5", N: 1})
	if err != nil || len(imgs) != 1 {
		t.Fatalf("images: %+v %v", imgs, err)
	}
	if got["parameters"].(map[string]any)["aspectRatio"] != "3:4" {
		t.Fatalf("imagen has no 4:5, expected 3:4: %v", got)
	}
	if header.Get("Authorization") != "Bearer tok" || header.Get("x-goog-user-project") != "p1" {
		t.Fatalf("headers: %v", header)
	}
}
