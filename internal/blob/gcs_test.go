package blob

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fsouza/fake-gcs-server/fakestorage"
)

func newFakeGCS(t *testing.T, prefix string) *GCS {
	t.Helper()
	srv, err := fakestorage.NewServerWithOptions(fakestorage.Options{Scheme: "http", NoListener: false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	srv.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: "b"})
	return NewGCSWithClient(srv.Client(), "b", prefix)
}

func TestGCSRoundTrip(t *testing.T) {
	ctx := context.Background()
	g := newFakeGCS(t, "media/")
	if g.Kind() != KindGCS {
		t.Fatal(g.Kind())
	}
	data := []byte("0123456789abcdef")
	if err := g.Put(ctx, "x1", data); err != nil {
		t.Fatal(err)
	}
	rc, size, err := g.Open(ctx, "x1")
	if err != nil || size != int64(len(data)) {
		t.Fatalf("open: %v size=%d", err, size)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != string(data) {
		t.Fatalf("got %q", got)
	}
	if n, err := rc.Seek(10, io.SeekStart); err != nil || n != 10 {
		t.Fatal(n, err)
	}
	buf := make([]byte, 3)
	if _, err := io.ReadFull(rc, buf); err != nil || string(buf) != "abc" {
		t.Fatalf("after seek: %q %v", buf, err)
	}
	if _, err := rc.Seek(-2, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	tail, _ := io.ReadAll(rc)
	if string(tail) != "ef" {
		t.Fatalf("tail %q", tail)
	}
	if _, err := rc.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("negative seek accepted")
	}
	// overwrite
	if err := g.Put(ctx, "x1", []byte("new")); err != nil {
		t.Fatal(err)
	}
	rc2, size, _ := g.Open(ctx, "x1")
	defer rc2.Close()
	if size != 3 {
		t.Fatalf("size after overwrite %d", size)
	}
}

func TestGCSNotFoundAndDelete(t *testing.T) {
	ctx := context.Background()
	g := newFakeGCS(t, "")
	if _, _, err := g.Open(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("open: %v", err)
	}
	if err := g.Delete(ctx, "nope"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	_ = g.Put(ctx, "a", []byte("1"))
	if err := g.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Open(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := g.Put(ctx, "../x", nil); err == nil {
		t.Fatal("bad id accepted")
	}
}

func TestGCSSignedURL(t *testing.T) {
	g := newFakeGCS(t, "media")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	g.SetSigning("sa@p.iam.gserviceaccount.com", func(_ context.Context, id string, payload []byte) ([]byte, error) {
		if id != "sa@p.iam.gserviceaccount.com" {
			t.Errorf("accessID %q", id)
		}
		h := sha256.Sum256(payload)
		return rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	})
	var s Signer = g
	u, err := s.SignedURL(context.Background(), "x1", 10*time.Minute, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/b/media/x1", "X-Goog-Signature=", "X-Goog-Algorithm=GOOG4-RSA-SHA256", "response-content-type=image%2Fpng", "X-Goog-Expires=59"} {
		if !strings.Contains(u, want) {
			t.Errorf("url missing %q: %s", want, u)
		}
	}
}

func TestParseGCS(t *testing.T) {
	t.Setenv("STORAGE_EMULATOR_HOST", "127.0.0.1:1")
	s, err := Parse("gcs://my-bucket/a/b/")
	if err != nil {
		t.Fatal(err)
	}
	g, ok := s.(*GCS)
	if !ok || g.bucket != "my-bucket" || g.prefix != "a/b" {
		t.Fatalf("%#v", s)
	}
	if _, err := Parse("gcs:///x"); err == nil {
		t.Fatal("missing bucket accepted")
	}
	if s, err := Parse(""); s != nil || err != nil {
		t.Fatal("empty should be db")
	}
}
