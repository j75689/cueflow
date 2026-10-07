package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fsouza/fake-gcs-server/fakestorage"

	"github.com/j75689/cueflow/internal/api"
	"github.com/j75689/cueflow/internal/blob"
	"github.com/j75689/cueflow/internal/internalauth"
)

type fakeVerifier struct{}

func (fakeVerifier) Verify(_ context.Context, bearer string) (string, error) {
	if bearer == "render-token" {
		return "cueflow-render@proj.iam.gserviceaccount.com", nil
	}
	return "", errors.New("bad token")
}

var mp4Bytes = append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00"), bytes.Repeat([]byte{1}, 4096)...)

func register(t *testing.T, url, token, object string) (int, mediaOut) {
	t.Helper()
	req, _ := http.NewRequest("POST", url+"/internal/media/register", strings.NewReader(`{"object":"`+object+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out mediaOut
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestMediaRegisterAndDelete(t *testing.T) {
	e := setup(t)
	gs, err := fakestorage.NewServerWithOptions(fakestorage.Options{Scheme: "http"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gs.Stop)
	gs.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: "b"})
	e.store.SetBlobs(blob.NewGCSWithClient(gs.Client(), "b", "media/"))

	// A second API front end over the same store, with the register route on.
	srv := httptest.NewServer(e.apiHandler(func(s *api.Server) {
		s.MediaRegisterAuth = internalauth.Middleware(fakeVerifier{})
	}))
	t.Cleanup(srv.Close)

	put := func(name string, data []byte) {
		gs.CreateObject(fakestorage.Object{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: "b", Name: name}, Content: data})
	}

	// Auth.
	put("uploads/a.mp4", mp4Bytes)
	if code, _ := register(t, srv.URL, "", "uploads/a.mp4"); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := register(t, srv.URL, "wrong", "uploads/a.mp4"); code != 401 && code != 403 {
		t.Fatalf("wrong token: %d", code)
	}
	// Bad input.
	put("uploads/note.mp4", []byte("this is text, not a video"))
	put("other/b.mp4", mp4Bytes)
	for _, obj := range []string{"uploads/missing.mp4", "uploads/note.mp4", "other/b.mp4", "uploads/../media/x", ""} {
		if code, _ := register(t, srv.URL, "render-token", obj); code != 400 {
			t.Fatalf("register %q: %d", obj, code)
		}
	}
	if _, err := gs.GetObject("b", "uploads/note.mp4"); err != nil {
		t.Fatal("a refused file must stay staged for the bucket lifecycle to remove")
	}

	// Success: served as .mp4, the staged copy is gone.
	code, m := register(t, srv.URL, "render-token", "uploads/a.mp4")
	if code != 201 || !strings.HasSuffix(m.URL, ".mp4") || m.MimeType != "video/mp4" {
		t.Fatalf("register: %d %+v", code, m)
	}
	if _, err := gs.GetObject("b", "uploads/a.mp4"); err == nil {
		t.Fatal("staged file should have been moved")
	}
	if code, _ := register(t, srv.URL, "render-token", "uploads/a.mp4"); code != 400 {
		t.Fatalf("registering twice: %d", code)
	}
	resp, err := http.Get(srv.URL + m.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 && resp.StatusCode != 302 && resp.StatusCode != 307 {
		t.Fatalf("serve: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// A draft that uses it blocks deletion; once rejected it does not.
	var post struct {
		ID string `json:"id"`
	}
	e.do("POST", "/posts", map[string]any{"content": "hi", "platforms": []string{"threads"}, "media_urls": []string{m.URL}, "submit": true}, &post)
	if code := e.do("DELETE", "/media/"+m.ID, nil, nil); code != 409 {
		t.Fatalf("delete in use: %d", code)
	}
	if n, err := e.store.PruneMedia(context.Background(), 0, true); err != nil || n != 0 {
		t.Fatalf("prune dry run with a used item: %d %v", n, err)
	}
	if code := e.do("POST", "/posts/"+post.ID+"/reject", map[string]any{"reason": "no"}, nil); code != 200 && code != 204 {
		t.Fatalf("reject: %d", code)
	}
	if n, err := e.store.PruneMedia(context.Background(), 24*time.Hour, false); err != nil || n != 0 {
		t.Fatalf("prune keeps recent media: %d %v", n, err)
	}
	if n, err := e.store.PruneMedia(context.Background(), 0, true); err != nil || n != 1 {
		t.Fatalf("dry run: %d %v", n, err)
	}
	if _, err := gs.GetObject("b", "media/"+m.ID); err != nil {
		t.Fatal("dry run must not delete")
	}
	if code := e.do("DELETE", "/media/"+m.ID+".mp4", nil, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if _, err := gs.GetObject("b", "media/"+m.ID); err == nil {
		t.Fatal("bucket object should be deleted")
	}
	if code := e.do("DELETE", "/media/"+m.ID, nil, nil); code != 404 {
		t.Fatalf("delete twice: %d", code)
	}

	// Prune removes old unused media and its object.
	put("uploads/c.mp4", mp4Bytes)
	_, c := register(t, srv.URL, "render-token", "uploads/c.mp4")
	time.Sleep(20 * time.Millisecond)
	if n, err := e.store.PruneMedia(context.Background(), 10*time.Millisecond, false); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
	if _, err := gs.GetObject("b", "media/"+c.ID); err == nil {
		t.Fatal("pruned object still in bucket")
	}
	_ = io.Discard
}

func TestMediaRegisterNeedsBucket(t *testing.T) {
	e := setup(t) // media in the database
	srv := httptest.NewServer(e.apiHandler(func(s *api.Server) {
		s.MediaRegisterAuth = internalauth.Middleware(fakeVerifier{})
	}))
	t.Cleanup(srv.Close)
	if code, _ := register(t, srv.URL, "render-token", "uploads/a.mp4"); code != 400 {
		t.Fatalf("db storage: %d", code)
	}
}
