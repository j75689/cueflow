package e2e

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/j75689/cueflow/internal/blob"
	"github.com/j75689/cueflow/internal/media"
	"github.com/j75689/cueflow/internal/store"
)

// A tiny valid GIF: media.Sniff accepts it as an image.
var tinyGIF = []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")

func TestMediaStorageBackendsAndMigrate(t *testing.T) {
	e := setup(t)
	ctx := t.Context()

	inDB, err := e.store.CreateMedia(ctx, store.NewMedia{MimeType: "image/gif", Data: tinyGIF, Source: "upload"})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	fb, err := blob.NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.store.SetBlobs(fb)
	onDisk, err := e.store.CreateMedia(ctx, store.NewMedia{MimeType: "image/gif", Data: tinyGIF, Source: "upload"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir + "/" + onDisk.ID.String()); err != nil {
		t.Fatalf("expected a file for the new media: %v", err)
	}

	// Both the old database row and the new file row are served, with Range support.
	for _, m := range []*store.Media{inDB, onDisk} {
		req, _ := http.NewRequest("GET", e.api.URL+media.URL(m.ID), nil)
		req.Header.Set("Range", "bytes=0-5")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(body, tinyGIF[:6]) {
			t.Fatalf("media %s: status %d body %q", m.ID, resp.StatusCode, body)
		}
	}

	// Migrating to the file backend moves the database row and empties its BYTEA.
	n, err := e.store.MigrateMedia(ctx, fb)
	if err != nil || n != 1 {
		t.Fatalf("migrate to file: moved %d, err %v", n, err)
	}
	var nullData bool
	if err := e.pool.QueryRow(ctx, `SELECT data IS NULL FROM media WHERE id=$1`, inDB.ID).Scan(&nullData); err != nil || !nullData {
		t.Fatalf("database copy not cleared: %v %v", nullData, err)
	}
	if m, err := e.store.GetMedia(ctx, inDB.ID); err != nil || !bytes.Equal(m.Data, tinyGIF) {
		t.Fatalf("read after migrate: %v", err)
	}

	// And back into the database.
	n, err = e.store.MigrateMedia(ctx, nil)
	if err != nil || n != 2 {
		t.Fatalf("migrate to db: moved %d, err %v", n, err)
	}
	e.store.SetBlobs(nil)
	if m, err := e.store.GetMedia(ctx, onDisk.ID); err != nil || !bytes.Equal(m.Data, tinyGIF) {
		t.Fatalf("read after migrate back: %v", err)
	}
}
