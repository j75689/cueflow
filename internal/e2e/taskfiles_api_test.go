package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/j75689/cueflow/internal/store"
)

// The file routes show a task its own files and its ancestors' (marked), page large text at
// character boundaries, download as an attachment sized from the row, and delete only the task's own.
func TestTaskFilesAPI(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	parent, _ := e.store.CreateTask(ctx, store.NewTask{Title: "Parent"})
	child, _ := e.store.CreateTask(ctx, store.NewTask{Title: "Child", ParentID: &parent.ID, CreatedBy: "agent"})
	other, _ := e.store.CreateTask(ctx, store.NewTask{Title: "Unrelated"})
	// 30,000 three-byte characters: 90,000 bytes, more than one 64 KB page.
	header := "# Result of read_url {\"url\":\"https://example.com\"}\n\n"
	big := header + strings.Repeat("字", 30000)
	if _, err := e.store.WriteTaskFile(ctx, parent.ID, "results/read_url-1.txt", "", []byte(big), "architect"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.WriteTaskFile(ctx, child.ID, "notes.md", "", []byte("child notes"), "reader"); err != nil {
		t.Fatal(err)
	}
	q := func(name string) string { return "?name=" + url.QueryEscape(name) }

	var list struct {
		Files []struct {
			Name, Description string
			CreatedBy         string `json:"created_by"`
			Own               bool
			TaskNumber        int64 `json:"task_number"`
			SizeBytes         int64 `json:"size_bytes"`
		} `json:"files"`
		Total int `json:"total"`
	}
	if code := e.do("GET", "/tasks/"+child.ID.String()+"/files", nil, &list); code != 200 || list.Total != 2 || len(list.Files) != 2 {
		t.Fatalf("list: %d %+v", code, list)
	}
	if f := list.Files[0]; f.Name != "notes.md" || !f.Own || f.CreatedBy != "reader" {
		t.Fatalf("own file: %+v", f)
	}
	if f := list.Files[1]; f.Own || f.TaskNumber != parent.Number || f.Description != `# Result of read_url {"url":"https://example.com"}` ||
		f.SizeBytes != int64(len(big)) {
		t.Fatalf("ancestor file: %+v", f)
	}
	// The parent doesn't see the child's file; an unrelated task sees neither.
	e.do("GET", "/tasks/"+parent.ID.String()+"/files", nil, &list)
	if list.Total != 1 {
		t.Fatalf("parent list: %+v", list)
	}
	if code := e.do("GET", "/tasks/"+other.ID.String()+"/files/content"+q("results/read_url-1.txt"), nil, nil); code != 404 {
		t.Fatalf("an unrelated task read another task's file: %d", code)
	}

	// Paging: pages join up to the whole text, each starts and ends on a character.
	var text strings.Builder
	offset, pages := 0, 0
	for {
		var page struct {
			Text       string `json:"text"`
			Binary     bool   `json:"binary"`
			Offset     int    `json:"offset"`
			NextOffset *int   `json:"next_offset"`
		}
		if code := e.do("GET", "/tasks/"+child.ID.String()+"/files/content"+q("results/read_url-1.txt")+"&offset="+strconv.Itoa(offset), nil, &page); code != 200 || page.Binary {
			t.Fatalf("page at %d: %d", offset, code)
		}
		if len(page.Text) > 64<<10+3 {
			t.Fatalf("a page of %d bytes", len(page.Text))
		}
		text.WriteString(page.Text)
		pages++
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}
	if text.String() != big || pages != 2 {
		t.Fatalf("pages rejoined to %d of %d bytes in %d pages", text.Len(), len(big), pages)
	}
	// An offset in the middle of a character starts at the next one.
	var mid struct {
		Text   string `json:"text"`
		Offset int    `json:"offset"`
	}
	e.do("GET", "/tasks/"+child.ID.String()+"/files/content"+q("results/read_url-1.txt")+"&offset="+strconv.Itoa(len(header)+4), nil, &mid)
	if !strings.HasPrefix(mid.Text, "字") || mid.Offset != len(header)+6 {
		t.Fatalf("mid-character offset: starts at %d with %q", mid.Offset, mid.Text[:6])
	}

	// Download: an attachment of the right length, never served as a page.
	req, _ := http.NewRequest("GET", e.api.URL+"/api/v1/tasks/"+child.ID.String()+"/files/download"+q("results/read_url-1.txt"), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != big || resp.ContentLength != int64(len(big)) ||
		resp.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment;") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "read_url-1.txt") {
		t.Fatalf("download: %d %d %v", resp.StatusCode, resp.ContentLength, resp.Header)
	}

	// Delete: the child can't delete its parent's file, can delete its own; bad names are refused.
	if code := e.do("DELETE", "/tasks/"+child.ID.String()+"/files"+q("results/read_url-1.txt"), nil, nil); code != 403 {
		t.Fatalf("child deleted its parent's file: %d", code)
	}
	if code := e.do("DELETE", "/tasks/"+child.ID.String()+"/files"+q("../x"), nil, nil); code != 400 {
		t.Fatalf("bad name: %d", code)
	}
	events, unsubscribe := e.server.Broker.Subscribe()
	defer unsubscribe()
	if code := e.do("DELETE", "/tasks/"+child.ID.String()+"/files"+q("notes.md"), nil, nil); code != 204 {
		t.Fatalf("delete own: %d", code)
	}
	// Open pages hear about it, scoped to the task's org like task.message.
	select {
	case ev := <-events:
		if ev.Name != "task.file" || ev.Org != child.OrgID.String() || !strings.Contains(fmt.Sprint(ev.Data), child.ID.String()) {
			t.Fatalf("event: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no task.file event after a delete")
	}
	if code := e.do("DELETE", "/tasks/"+child.ID.String()+"/files"+q("notes.md"), nil, nil); code != 404 {
		t.Fatalf("delete again: %d", code)
	}
	if code := e.do("GET", "/tasks/"+parent.ID.String()+"/files/content"+q("results/read_url-1.txt"), nil, nil); code != 200 {
		t.Fatalf("the parent's file should still be there: %d", code)
	}
}
