package e2e

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/blob"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

func readTaskFile(t *testing.T, e *env, task *store.Task, name string) (*store.TaskFile, string, error) {
	t.Helper()
	tf, rc, err := e.store.OpenTaskFile(context.Background(), task.ID, name)
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	return tf, string(b), err
}

// Task files live in the blob backend, are read by the task and its subtasks (nearest file wins),
// written only by their own task, never served on /media, and go away with the task, bytes included.
func TestTaskFiles(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	dir := t.TempDir()
	fs, err := blob.NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.store.SetBlobs(fs)

	parent, err := e.store.CreateTask(ctx, store.NewTask{Title: "Plan Disney+ support"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.store.CreateTask(ctx, store.NewTask{Title: "Read the hooks", ParentID: &parent.ID, CreatedBy: "agent"})
	if err != nil {
		t.Fatal(err)
	}

	notes, err := e.store.WriteTaskFile(ctx, parent.ID, "notes.md", "", []byte("v1"), "architect")
	if err != nil {
		t.Fatal(err)
	}
	if notes.SizeBytes != 2 || notes.MimeType == "" || notes.TaskID != parent.ID {
		t.Fatalf("file: %+v", notes)
	}
	if _, err := os.Stat(filepath.Join(dir, notes.ID.String())); err != nil {
		t.Fatalf("bytes not in the blob backend: %v", err)
	}
	// Replacing stores the new bytes under a fresh key and deletes the old ones after the commit.
	again, err := e.store.WriteTaskFile(ctx, parent.ID, "notes.md", "text/markdown", []byte("version two"), "architect")
	if err != nil || again.ID == notes.ID || again.SizeBytes != 11 || again.MimeType != "text/markdown" {
		t.Fatalf("replace: %+v %v", again, err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 || left[0].Name() != again.ID.String() {
		t.Fatalf("want only the new bytes in the backend, got %v", left)
	}
	// A replace that fails after its bytes were written leaves the old file whole.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.store.WriteTaskFile(canceled, parent.ID, "notes.md", "", []byte("never"), "architect"); err == nil {
		t.Fatal("a write with a canceled context succeeded")
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Fatalf("a failed replace left %d blobs", len(left))
	}
	if _, body, err := readTaskFile(t, e, parent, "notes.md"); err != nil || body != "version two" {
		t.Fatalf("read own: %q %v", body, err)
	}

	// A subtask reads its parent's file; writing the same name makes its own and leaves the parent's.
	if tf, body, err := readTaskFile(t, e, child, "notes.md"); err != nil || body != "version two" || tf.TaskID != parent.ID {
		t.Fatalf("read parent's from child: %q %v", body, err)
	}
	if _, err := e.store.WriteTaskFile(ctx, child.ID, "notes.md", "", []byte("child's"), "reader"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.WriteTaskFile(ctx, child.ID, "results/hook-1.txt", "", []byte("big result"), "reader"); err != nil {
		t.Fatal(err)
	}
	if tf, body, _ := readTaskFile(t, e, child, "notes.md"); body != "child's" || tf.TaskID != child.ID {
		t.Fatalf("child should see its own file first: %q", body)
	}
	if _, body, _ := readTaskFile(t, e, parent, "notes.md"); body != "version two" {
		t.Fatalf("the parent's file changed: %q", body)
	}
	// The parent doesn't see its subtask's files.
	if _, _, err := readTaskFile(t, e, parent, "results/hook-1.txt"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("parent read child's file: %v", err)
	}
	list, total, err := e.store.ListTaskFiles(ctx, child.ID, 10)
	if err != nil || total != 2 || len(list) != 2 || list[0].Name != "notes.md" || list[0].TaskID != child.ID || list[1].Name != "results/hook-1.txt" ||
		string(list[0].Head) != "child's" || list[0].TaskNumber != child.Number {
		t.Fatalf("list: %+v %d %v", list, total, err)
	}
	// The limit caps the rows, not the total.
	if one, total, err := e.store.ListTaskFiles(ctx, child.ID, 1); err != nil || len(one) != 1 || total != 2 {
		t.Fatalf("limited list: %d of %d (%v)", len(one), total, err)
	}

	// Bad input is a validation error (400), never a database error (500), and writes no bytes.
	before, _ := os.ReadDir(dir)
	var invalid *store.ValidationError
	for _, bad := range []string{"", "/abs", "a//b", "../up", "a/./b", `win\path`, "tab\there", "notes\xff.md", strings.Repeat("字", 201)} {
		if _, err := e.store.WriteTaskFile(ctx, parent.ID, bad, "", []byte("x"), "a"); !errors.As(err, &invalid) {
			t.Errorf("name %q: %v", bad, err)
		}
	}
	if _, err := e.store.WriteTaskFile(ctx, parent.ID, "doc.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", []byte("x"), "a"); !errors.As(err, &invalid) {
		t.Errorf("a 71-character MIME type: %v", err)
	}
	if _, err := e.store.WriteTaskFile(ctx, parent.ID, "a.txt", "", []byte("x"), strings.Repeat("a", 65)); !errors.As(err, &invalid) {
		t.Errorf("a 65-character author: %v", err)
	}
	// 200 characters is the limit, however many bytes they take.
	long := strings.Repeat("字", 200)
	if _, err := e.store.WriteTaskFile(ctx, parent.ID, long, "", []byte("x"), "a"); err != nil {
		t.Fatalf("a 200-character name: %v", err)
	}
	if err := e.store.DeleteTaskFile(ctx, parent.ID, long); err != nil {
		t.Fatal(err)
	}
	// An unknown task is not found, and its bytes don't stay behind.
	if _, err := e.store.WriteTaskFile(ctx, uuid.New(), "a.txt", "", []byte("x"), "a"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown task: %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != len(before) {
		t.Fatalf("refused writes left blobs: %d before, %d after", len(before), len(left))
	}
	if _, err := e.store.WriteTaskFile(ctx, parent.ID, "huge", "", make([]byte, store.MaxTaskFileBytes+1), "a"); err == nil {
		t.Error("an oversized file was accepted")
	}

	// The public media route never serves a task file.
	resp, err := http.Get(e.api.URL + "/media/" + notes.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/media served a task file: %d", resp.StatusCode)
	}

	// Media still goes through the same byte layer.
	m, err := e.store.CreateMedia(ctx, store.NewMedia{MimeType: "image/png", Data: []byte("png"), Source: "upload"})
	if err != nil {
		t.Fatal(err)
	}
	if _, rc, err := e.store.OpenMedia(ctx, m.ID); err != nil {
		t.Fatal(err)
	} else {
		b, _ := io.ReadAll(rc)
		rc.Close()
		if string(b) != "png" {
			t.Fatalf("media bytes: %q", b)
		}
	}
	if err := e.store.DeleteMedia(ctx, m.ID); err != nil {
		t.Fatal(err)
	}

	// Deleting one file, then the whole task: the cascade takes the subtask, and every blob goes too.
	if err := e.store.DeleteTaskFile(ctx, child.ID, "notes.md"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.DeleteTask(ctx, parent.ID); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 0 {
		t.Fatalf("%d blobs left after deleting the task", len(left))
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM task_files`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d task_files rows left (%v)", n, err)
	}
}

// Without a blob backend, task files live in the row, the same as media.
func TestTaskFilesInDatabase(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	task, err := e.store.CreateTask(ctx, store.NewTask{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.WriteTaskFile(ctx, task.ID, "a.txt", "", []byte("in the row"), "a"); err != nil {
		t.Fatal(err)
	}
	if _, body, err := readTaskFile(t, e, task, "a.txt"); err != nil || body != "in the row" {
		t.Fatalf("%q %v", body, err)
	}
	if err := e.store.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
}

// failingDeletes is a blob backend whose deletes fail while fail is set.
type failingDeletes struct {
	blob.Store
	fail bool
}

func (f *failingDeletes) Delete(ctx context.Context, id string) error {
	if f.fail {
		return errors.New("bucket unavailable")
	}
	return f.Store.Delete(ctx, id)
}

// A blob that can't be deleted rolls the whole delete back: the task and its file rows stay, nothing
// points at missing bytes it shouldn't, and retrying once the backend is back finishes the job.
func TestDeleteTaskRollsBackWhenBlobsFail(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	dir := t.TempDir()
	fs, err := blob.NewFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	flaky := &failingDeletes{Store: fs}
	e.store.SetBlobs(flaky)
	task, err := e.store.CreateTask(ctx, store.NewTask{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.WriteTaskFile(ctx, task.ID, "a.txt", "", []byte("keep me"), "a"); err != nil {
		t.Fatal(err)
	}

	flaky.fail = true
	if err := e.store.DeleteTask(ctx, task.ID); err == nil {
		t.Fatal("the delete reported success although a blob could not be deleted")
	}
	if _, err := e.store.GetTask(ctx, task.ID); err != nil {
		t.Fatalf("the task is gone after a failed delete: %v", err)
	}
	if _, body, err := readTaskFile(t, e, task, "a.txt"); err != nil || body != "keep me" {
		t.Fatalf("the file is gone after a failed delete: %q %v", body, err)
	}
	if err := e.store.DeleteTaskFile(ctx, task.ID, "a.txt"); err == nil {
		t.Fatal("deleting one file reported success although its blob could not be deleted")
	}
	if _, _, err := readTaskFile(t, e, task, "a.txt"); err != nil {
		t.Fatalf("the file row is gone after a failed file delete: %v", err)
	}

	flaky.fail = false
	if err := e.store.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("%d blobs left", len(left))
	}
	if _, err := e.store.GetTask(ctx, task.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("task still there: %v", err)
	}
}
