package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

var testPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDRfake-image-bytes")

// fakeImage plays an image connector and counts generations.
type fakeImage struct{}

var imagesMade atomic.Int32

func (fakeImage) Test(context.Context) (*connector.TestResult, error) {
	return &connector.TestResult{OK: true}, nil
}

func (fakeImage) GenerateImage(_ context.Context, req connector.ImageRequest) ([]connector.Image, error) {
	imagesMade.Add(1)
	return []connector.Image{{Data: testPNG, MimeType: "image/png"}}, nil
}

func init() {
	connector.Register(connector.Spec{Type: "fake_image", Kind: connector.KindImage, Name: "Fake images"},
		func(connector.Config, connector.Env) (connector.Instance, error) { return fakeImage{}, nil })
}

type mediaOut struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	MimeType string `json:"mime_type"`
	Source   string `json:"source"`
}

func TestMediaUploadGenerateAndServe(t *testing.T) {
	e := setup(t)

	// Upload.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "a.png")
	_, _ = fw.Write(testPNG)
	_ = mw.Close()
	resp, err := http.Post(e.api.URL+"/api/v1/media", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	var up mediaOut
	_ = json.NewDecoder(resp.Body).Decode(&up)
	resp.Body.Close()
	if resp.StatusCode != 201 || !strings.HasPrefix(up.URL, "/media/") || up.MimeType != "image/png" || up.Source != "upload" {
		t.Fatalf("upload: %d %+v", resp.StatusCode, up)
	}
	got, err := http.Get(e.api.URL + up.URL)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(got.Body)
	got.Body.Close()
	if got.StatusCode != 200 || got.Header.Get("Content-Type") != "image/png" || !bytes.Equal(data, testPNG) {
		t.Fatalf("serve: %d %s", got.StatusCode, got.Header.Get("Content-Type"))
	}

	// Not an image.
	body.Reset()
	mw = multipart.NewWriter(&body)
	fw, _ = mw.CreateFormFile("file", "a.txt")
	_, _ = fw.Write([]byte("hello, not an image"))
	_ = mw.Close()
	resp, _ = http.Post(e.api.URL+"/api/v1/media", mw.FormDataContentType(), &body)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("text upload should be refused: %d", resp.StatusCode)
	}

	// Generate without an image connector, then with one.
	if code := e.do("POST", "/media/generate", map[string]any{"prompt": "a cat"}, nil); code != 400 {
		t.Fatalf("generate without connector: %d", code)
	}
	e.do("POST", "/connectors", map[string]any{"name": "Img", "type": "fake_image", "config": map[string]any{}}, nil)
	var gen mediaOut
	if code := e.do("POST", "/media/generate", map[string]any{"prompt": "a cat", "aspect": "4:5"}, &gen); code != 201 || gen.Source != "generated" {
		t.Fatalf("generate: %d %+v", code, gen)
	}
	if code := e.do("POST", "/media/generate", map[string]any{"prompt": "a cat", "aspect": "2:1"}, nil); code != 400 {
		t.Fatalf("bad aspect: %d", code)
	}

	// Publishers get an absolute URL and the bytes of stored media.
	var pub store.Connector
	e.do("POST", "/connectors", map[string]any{"name": "Accounts", "type": "fake_accounts", "config": map[string]any{}}, &pub)
	e.worker.MediaBaseURL = "http://cueflow:3000"
	var post domain.Post
	e.do("POST", "/posts", map[string]any{"content": "hi", "platforms": []string{"threads"}, "media_urls": []string{gen.URL, "https://cdn.example.com/x.png"},
		"publisher_connector_id": pub.ID, "submit": true}, &post)
	accountsMu.Lock()
	accountsReqs = nil
	accountsMu.Unlock()
	e.do("POST", "/posts/"+post.ID.String()+"/approve", map[string]any{}, nil)
	e.worker.Tick(context.Background())
	accountsMu.Lock()
	defer accountsMu.Unlock()
	if len(accountsReqs) == 0 {
		t.Fatal("publisher not called")
	}
	r := accountsReqs[0]
	if r.MediaURLs[0] != "http://cueflow:3000"+gen.URL || r.MediaURLs[1] != "https://cdn.example.com/x.png" {
		t.Fatalf("urls: %v", r.MediaURLs)
	}
	if !r.Media[0].Stored || r.Media[1].Stored {
		t.Fatalf("stored flags: %+v", r.Media)
	}
	b, mt, err := r.Media[0].Load(context.Background())
	if err != nil || mt != "image/png" || !bytes.Equal(b, testPNG) {
		t.Fatalf("load: %s %v", mt, err)
	}
}

var uuidRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func TestWriterGeneratesImageWithinLimit(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	e.do("POST", "/connectors", map[string]any{"name": "Img", "type": "fake_image", "config": map[string]any{}}, nil)
	imagesMade.Store(0)

	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		res := toolResults(req)
		switch len(res) {
		case 0:
			return calls("create_post_draft", map[string]any{"platform": "instagram", "content": "Look at this",
				"image_prompt": "a red fox in snow, photo"})
		case 1, 2, 3:
			id := uuidRe.FindString(res[0].Content)
			return calls("generate_image", map[string]any{"post_id": id})
		default:
			var all []string
			for _, r := range res {
				all = append(all, r.Content)
			}
			return calls("complete_task", map[string]any{"summary": strings.Join(all, " | ")})
		}
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "IG post", "assignee_agent_id": team["writer"].ID}, &task)
	e.crew.RunPending(ctx, 3)

	got, _ := e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskReview || !strings.Contains(got.Result, "image limit reached") {
		t.Fatalf("task: %s %q", got.Status, got.Result)
	}
	if n := imagesMade.Load(); n != 2 {
		t.Fatalf("images generated: %d, want 2 (the per-run limit)", n)
	}
	posts, _ := e.store.PostsForTask(ctx, task.ID)
	if len(posts) != 1 {
		t.Fatalf("posts: %+v", posts)
	}
	p, _ := e.store.GetPost(ctx, posts[0].ID)
	if len(p.MediaURLs) != 2 || !strings.HasPrefix(p.MediaURLs[0], "/media/") || p.Status != domain.StatusPendingReview {
		t.Fatalf("draft media: %v %s", p.MediaURLs, p.Status)
	}
}
