package blob

import (
	"context"
	"errors"
	"io"
	"testing"
)

// stage writes a file straight into the bucket's staging area, as the render service does.
func stage(t *testing.T, g *GCS, name string, data []byte) {
	t.Helper()
	w := g.client.Bucket(g.bucket).Object(name).NewWriter(context.Background())
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGCSStatAndAdopt(t *testing.T) {
	ctx := context.Background()
	g := newFakeGCS(t, "media/")
	var _ Adopter = g
	data := append([]byte("\x00\x00\x00\x18ftypmp42"), make([]byte, 2000)...)
	stage(t, g, "uploads/a.mp4", data)

	st, err := g.Stat(ctx, "uploads/a.mp4")
	if err != nil || st.Size != int64(len(data)) || len(st.Head) != 512 || string(st.Head[4:8]) != "ftyp" {
		t.Fatalf("stat: %+v %v", st, err)
	}
	if err := g.Adopt(ctx, "uploads/a.mp4", "id1"); err != nil {
		t.Fatal(err)
	}
	rc, size, err := g.Open(ctx, "id1")
	if err != nil || size != int64(len(data)) {
		t.Fatalf("open: %d %v", size, err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if len(got) != len(data) {
		t.Fatalf("adopted %d bytes", len(got))
	}
	if _, err := g.Stat(ctx, "uploads/a.mp4"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("staged file should be gone: %v", err)
	}
	if err := g.Adopt(ctx, "uploads/a.mp4", "id2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("adopt twice: %v", err)
	}
}

func TestGCSStagedNameValidation(t *testing.T) {
	ctx := context.Background()
	g := newFakeGCS(t, "media/")
	for _, name := range []string{"", "a.mp4", "media/x", "uploads/", "uploads/../x", "uploads/a/b", `uploads/a\b`, "uploads/.", "uploads/.."} {
		if _, err := g.Stat(ctx, name); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Stat(%q) = %v, want a validation error", name, err)
		}
		if err := g.Adopt(ctx, name, "x"); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Adopt(%q) = %v, want a validation error", name, err)
		}
	}
}

func TestGCSStatEmptyAndMissing(t *testing.T) {
	ctx := context.Background()
	g := newFakeGCS(t, "")
	if _, err := g.Stat(ctx, "uploads/none.mp4"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	stage(t, g, "uploads/empty.mp4", nil)
	st, err := g.Stat(ctx, "uploads/empty.mp4")
	if err != nil || st.Size != 0 || len(st.Head) != 0 {
		t.Fatalf("%+v %v", st, err)
	}
}
