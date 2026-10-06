package cuesocial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/connector"
)

// fakeCueSocial mimics CueSocial's MCP tools. createStatus decides how create_post ends.
type fakeCueSocial struct {
	mu           sync.Mutex
	createStatus string
	block        chan struct{}
	calls        []string
	lastCreate   map[string]any
	headers      http.Header
	localMedia   bool   // create_upload refuses, like a CueSocial that keeps media locally
	uploaded     []byte // body received at the signed upload URL
	uploadHeader http.Header
}

func (f *fakeCueSocial) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

func jsonResult(v any) *mcp.CallToolResult {
	b, _ := json.Marshal(v)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

func (f *fakeCueSocial) server(t *testing.T) *httptest.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "cuesocial", Version: "test"}, nil)
	srv.AddTool(&mcp.Tool{Name: "list_accounts", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			f.record("list_accounts")
			return jsonResult(map[string]any{"accounts": []map[string]any{
				{"id": "th1", "platform": "threads", "username": "brand", "tag": "brand"},
				{"id": "th2", "platform": "threads", "username": "me"},
				{"id": "ig1", "platform": "instagram", "username": "brand.ig"},
				{"id": "th9", "platform": "threads", "username": "sandbox", "tag": "test"},
			}}), nil
		})
	srv.AddTool(&mcp.Tool{Name: "create_post", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			f.record("create_post")
			var in map[string]any
			_ = json.Unmarshal(req.Params.Arguments, &in)
			f.mu.Lock()
			f.lastCreate = in
			status, block := f.createStatus, f.block
			f.mu.Unlock()
			if block != nil {
				select {
				case <-block:
				case <-ctx.Done():
				}
			}
			if status == "rejected" {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "Instagram needs an image"}}}, nil
			}
			results := []map[string]any{{"account": "th1", "label": "threads @brand", "published": true, "permalink": "https://threads.net/p/1"}}
			if status == "partial" {
				results = append(results, map[string]any{"account": "th2", "label": "threads @me", "published": false, "error": "token expired"})
			}
			if status == "scheduled" {
				results = nil
			}
			return jsonResult(map[string]any{"id": "post-1", "status": status, "results": results}), nil
		})
	var base string
	srv.AddTool(&mcp.Tool{Name: "create_upload", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			f.record("create_upload")
			if f.localMedia {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "this server keeps media locally and can't take direct uploads"}}}, nil
			}
			var in struct {
				ContentType string `json:"content_type"`
				Size        int    `json:"size"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &in)
			return jsonResult(map[string]any{"upload_url": base + "/upload/vid-1", "method": "PUT", "key": "uploads/vid-1.mp4",
				"headers": map[string]string{"Content-Type": in.ContentType, "X-Goog-Content-Length-Range": fmt.Sprintf("0,%d", in.Size)}}), nil
		})
	srv.AddTool(&mcp.Tool{Name: "publish_post", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			f.record("publish_post")
			return jsonResult(map[string]any{"id": "post-1", "status": "published", "results": []map[string]any{
				{"account": "th1", "label": "threads @brand", "published": true, "permalink": "https://threads.net/p/1"},
				{"account": "th2", "label": "threads @me", "published": true, "permalink": "https://threads.net/p/2"},
			}}), nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/upload/") {
			body, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.uploaded, f.uploadHeader = body, r.Header.Clone()
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		f.mu.Lock()
		f.headers = r.Header.Clone()
		f.mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	base = ts.URL
	return ts
}

func newPublisher(t *testing.T, f *fakeCueSocial) *Publisher {
	ts := f.server(t)
	inst, err := New(connector.Config{"endpoint_url": ts.URL, "cf_client_id": "cid", "cf_client_secret": "csecret"},
		connector.Env{HTTPClient: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return inst.(*Publisher)
}

func req(accounts ...string) connector.PublishRequest {
	return connector.PublishRequest{PostID: uuid.New(), Platforms: []string{"threads"}, Parts: []string{"Hello"},
		Hashtags: []string{"mcp"}, Accounts: accounts}
}

func TestTestAndAccounts(t *testing.T) {
	f := &fakeCueSocial{createStatus: "published"}
	p := newPublisher(t, f)
	res, _ := p.Test(context.Background())
	if !res.OK || !strings.Contains(res.Message, "4 accounts") || !strings.Contains(res.Message, "threads @brand") {
		t.Fatalf("test: %+v", res)
	}
	if f.headers.Get("CF-Access-Client-Id") != "cid" || f.headers.Get("CF-Access-Client-Secret") != "csecret" {
		t.Fatalf("access headers not sent: %v", f.headers)
	}
	accts, err := p.ListAccounts(context.Background())
	if err != nil || len(accts) != 4 || accts[2] != (connector.Account{ID: "ig1", Platform: "instagram", Name: "@brand.ig"}) {
		t.Fatalf("accounts: %+v %v", accts, err)
	}
}

func TestPublish(t *testing.T) {
	f := &fakeCueSocial{createStatus: "published"}
	p := newPublisher(t, f)
	res, err := p.Publish(context.Background(), req("th1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalIDs[PostIDKey] != "post-1" || res.ExternalIDs["threads @brand"] != "https://threads.net/p/1" {
		t.Fatalf("ids: %v", res.ExternalIDs)
	}
	parts := f.lastCreate["parts"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "Hello\n\n#mcp" || f.lastCreate["scheduled_at"] != nil {
		t.Fatalf("create args: %v", f.lastCreate)
	}
}

func TestPublishValidation(t *testing.T) {
	f := &fakeCueSocial{createStatus: "published"}
	p := newPublisher(t, f)
	cases := map[string]connector.PublishRequest{
		"no CueSocial accounts chosen": req(),
		"no longer connected":          req("gone"),
		"not one of this post's":       req("ig1"),
	}
	long := req("th1")
	long.Parts = []string{"ok", strings.Repeat("字", 501)}
	cases["characters per part; part 2"] = long
	tooMany := req("th1")
	tooMany.Parts = make([]string, 11)
	cases["at most 10 parts"] = tooMany
	igNoImage := req("ig1")
	igNoImage.Platforms = []string{"instagram"}
	cases["needs at least one image"] = igNoImage
	igMany := req("ig1")
	igMany.Platforms = []string{"instagram"}
	for i := 0; i < 11; i++ {
		igMany.MediaURLs = append(igMany.MediaURLs, "https://cdn.example.com/x.png")
	}
	cases["at most 10 images"] = igMany
	webp := req("th1")
	webp.MediaURLs = []string{"/media/x"}
	webp.Media = []connector.MediaFile{{URL: "/media/x", Stored: true,
		Load: func(context.Context) ([]byte, string, error) { return []byte("RIFF"), "image/webp", nil }}}
	cases["accepts JPEG or PNG"] = webp
	img := req("th1")
	img.MediaURLs = []string{"http://10.0.0.1/a.png"}
	cases["public https URL"] = img
	for want, r := range cases {
		if _, err := p.Publish(context.Background(), r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
	for _, c := range f.calls {
		if c == "create_post" {
			t.Fatal("invalid posts must not reach create_post")
		}
	}
}

func TestScheduled(t *testing.T) {
	f := &fakeCueSocial{createStatus: "scheduled"}
	p := newPublisher(t, f)
	r := req("th1", "th2")
	at := time.Now().Add(time.Hour)
	r.ScheduledAt = &at
	r.MediaURLs = []string{"https://cdn.example.com/a.png"}
	res, err := p.Publish(context.Background(), r)
	if err != nil || res.Details["cuesocial_status"] != "scheduled" {
		t.Fatalf("scheduled: %+v %v", res, err)
	}
	part := f.lastCreate["parts"].([]any)[0].(map[string]any)
	if f.lastCreate["scheduled_at"] != at.UTC().Format(time.RFC3339) || part["image_urls"].([]any)[0] != "https://cdn.example.com/a.png" {
		t.Fatalf("create args: %v", f.lastCreate)
	}
}

func TestPartialThenRetryUsesPublishPost(t *testing.T) {
	f := &fakeCueSocial{createStatus: "partial"}
	p := newPublisher(t, f)
	_, err := p.Publish(context.Background(), req("th1", "th2"))
	var partial *connector.PartialError
	if !errors.As(err, &partial) || partial.ExternalIDs[PostIDKey] != "post-1" || !strings.Contains(err.Error(), "token expired") {
		t.Fatalf("partial: %v", err)
	}
	r := req("th1", "th2")
	r.Previous = partial.ExternalIDs
	res, err := p.Publish(context.Background(), r)
	if err != nil || res.ExternalIDs["threads @me"] != "https://threads.net/p/2" {
		t.Fatalf("retry: %+v %v", res, err)
	}
	creates := 0
	for _, c := range f.calls {
		if c == "create_post" {
			creates++
		}
	}
	if creates != 1 || f.calls[len(f.calls)-1] != "publish_post" {
		t.Fatalf("retry must call publish_post, not create_post again: %v", f.calls)
	}
}

func TestRejectedIsPlainError(t *testing.T) {
	f := &fakeCueSocial{createStatus: "rejected"}
	p := newPublisher(t, f)
	_, err := p.Publish(context.Background(), req("th1"))
	var partial *connector.PartialError
	if err == nil || errors.As(err, &partial) || !strings.Contains(err.Error(), "needs an image") {
		t.Fatalf("rejected: %v", err)
	}
}

func TestTimeoutWarnsItMayHaveGoneOut(t *testing.T) {
	f := &fakeCueSocial{createStatus: "published", block: make(chan struct{})}
	defer close(f.block)
	p := newPublisher(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := p.Publish(ctx, req("th1"))
	var partial *connector.PartialError
	if err == nil || errors.As(err, &partial) || !strings.Contains(err.Error(), "may have been published") {
		t.Fatalf("timeout: %v", err)
	}
}

func TestStoredImageIsUploaded(t *testing.T) {
	f := &fakeCueSocial{createStatus: "published"}
	p := newPublisher(t, f)
	r := req("th1")
	r.MediaURLs = []string{"http://cueflow:3000/media/abc"}
	r.Media = []connector.MediaFile{{URL: r.MediaURLs[0], Stored: true,
		Load: func(context.Context) ([]byte, string, error) { return []byte("PNGDATA"), "image/png", nil }}}
	if _, err := p.Publish(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	part := f.lastCreate["parts"].([]any)[0].(map[string]any)
	if part["images_base64"].([]any)[0] != "UE5HREFUQQ==" || part["image_urls"] != nil {
		t.Fatalf("stored media should be sent as base64: %v", f.lastCreate)
	}
}

func TestThreadWithImages(t *testing.T) {
	f := &fakeCueSocial{createStatus: "published"}
	p := newPublisher(t, f)
	r := req("th1", "ig1")
	r.Platforms = []string{"threads", "instagram"}
	r.Parts = []string{"one", "two", "three"}
	r.MediaURLs = []string{"https://cdn.example.com/a.png", "http://cueflow:3000/media/b"}
	r.Media = []connector.MediaFile{{URL: r.MediaURLs[0]}, {URL: r.MediaURLs[1], Stored: true,
		Load: func(context.Context) ([]byte, string, error) { return []byte("JPG"), "image/jpeg", nil }}}
	if _, err := p.Publish(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	parts := f.lastCreate["parts"].([]any)
	if len(parts) != 3 {
		t.Fatalf("parts: %v", parts)
	}
	first, last := parts[0].(map[string]any), parts[2].(map[string]any)
	if first["text"] != "one" || last["text"] != "three\n\n#mcp" {
		t.Fatalf("texts: %v", parts)
	}
	if len(first["image_urls"].([]any)) != 1 || len(first["images_base64"].([]any)) != 1 || parts[1].(map[string]any)["image_urls"] != nil {
		t.Fatalf("all images belong on the first part: %v", parts)
	}
}

func TestTopicAndLink(t *testing.T) {
	f := &fakeCueSocial{createStatus: "published"}
	p := newPublisher(t, f)
	r := req("th1")
	r.Topic, r.Link = "#日文學習", "https://futureindex.ai/blog/1"
	if _, err := p.Publish(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if f.lastCreate["topic"] != "日文學習" || f.lastCreate["link"] != "https://futureindex.ai/blog/1" {
		t.Fatalf("extras: %v", f.lastCreate)
	}
	accts, _ := p.ListAccounts(context.Background())
	if accts[0].Tag != "brand" {
		t.Fatalf("account tag: %+v", accts[0])
	}

	bad := map[string]connector.PublishRequest{}
	b := req("th1")
	b.Topic = "a.b"
	bad["can't contain"] = b
	b = req("th1")
	b.Topic = strings.Repeat("字", 51)
	bad["at most 50"] = b
	b = req("th1")
	b.Link = "futureindex.ai"
	bad["full http(s) URL"] = b
	b = req("th1")
	b.Link = "https://futureindex.ai"
	b.MediaURLs = []string{"https://cdn.example.com/a.png"}
	bad["part without images"] = b
	for want, r := range bad {
		if _, err := p.Publish(context.Background(), r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
	// A text-only second part can carry the card while the first holds the image.
	ok := req("th1")
	ok.Parts = []string{"with image", "read more"}
	ok.Link = "https://futureindex.ai"
	ok.MediaURLs = []string{"https://cdn.example.com/a.png"}
	if _, err := p.Publish(context.Background(), ok); err != nil {
		t.Fatalf("link on a text-only part: %v", err)
	}
}

func TestStoredVideoUpload(t *testing.T) {
	video := []byte("\x00\x00\x00\x18ftypmp42-fake-video")
	mk := func(f *fakeCueSocial, public string) *Publisher {
		ts := f.server(t)
		inst, _ := New(connector.Config{"endpoint_url": ts.URL, "public_media_base_url": public}, connector.Env{HTTPClient: ts.Client()})
		return inst.(*Publisher)
	}
	r := req("ig1")
	r.Platforms = []string{"instagram"}
	r.MediaURLs = []string{"http://cueflow:3000/media/abc.mp4"}
	r.Media = []connector.MediaFile{{URL: r.MediaURLs[0], Stored: true,
		Load: func(context.Context) ([]byte, string, error) { return video, "video/mp4", nil }}}

	// Default: uploaded to CueSocial's signed URL, then referenced by key.
	f := &fakeCueSocial{createStatus: "published"}
	if _, err := mk(f, "").Publish(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	part := f.lastCreate["parts"].([]any)[0].(map[string]any)
	if k := part["video_keys"].([]any); len(k) != 1 || k[0] != "uploads/vid-1.mp4" || part["video_urls"] != nil {
		t.Fatalf("video_keys: %v", part)
	}
	if string(f.uploaded) != string(video) || f.uploadHeader.Get("Content-Type") != "video/mp4" ||
		f.uploadHeader.Get("X-Goog-Content-Length-Range") != fmt.Sprintf("0,%d", len(video)) {
		t.Fatalf("upload: %q %v", f.uploaded, f.uploadHeader)
	}

	// A CueSocial without direct uploads fetches from the public media URL instead.
	f = &fakeCueSocial{createStatus: "published", localMedia: true}
	if _, err := mk(f, "https://media.example.com/").Publish(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	part = f.lastCreate["parts"].([]any)[0].(map[string]any)
	if v := part["video_urls"].([]any); len(v) != 1 || v[0] != "https://media.example.com/media/abc.mp4" {
		t.Fatalf("video_urls fallback: %v", part)
	}

	// Neither works: a clear error, nothing created.
	f = &fakeCueSocial{createStatus: "published", localMedia: true}
	if _, err := mk(f, "").Publish(context.Background(), r); err == nil || !strings.Contains(err.Error(), "upload video 1") {
		t.Fatalf("no upload, no public URL: %v", err)
	}
}

func TestTestAndRealAccountsDontMix(t *testing.T) {
	f := &fakeCueSocial{createStatus: "published"}
	p := newPublisher(t, f)
	if _, err := p.Publish(context.Background(), req("th1", "th9")); err == nil || !strings.Contains(err.Error(), "never mixes test accounts") {
		t.Fatalf("mixed accounts: %v", err)
	}
	if _, err := p.Publish(context.Background(), req("th9")); err != nil {
		t.Fatalf("test account alone: %v", err)
	}
}
