package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/blob"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

func readTaskFile(t *testing.T, e *env, task *store.Task, name string) (*store.TaskFile, string, error) {
	t.Helper()
	tf, rc, err := e.store.OpenTaskFile(context.Background(), task.ID, uuid.Nil, name)
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
	// A bare name never resolves to a subtask's file (TestSubtaskFiles reads them by owner).
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
	if n, err := e.db.Count(ctx, "task_files", nil); err != nil || n != 0 {
		t.Fatalf("%d task_files rows left (%v)", n, err)
	}
}

// A task lists its direct subtasks' files after its own, not deduped, and reads one only by naming
// the subtask. A bare name resolves as before; a sibling, a grandchild or an unrelated task is
// refused without reading anything.
func TestSubtaskFiles(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	newTask := func(title string, parent *store.Task) *store.Task {
		t.Helper()
		nt := store.NewTask{Title: title}
		if parent != nil {
			nt.ParentID, nt.CreatedBy = &parent.ID, "agent"
		}
		task, err := e.store.CreateTask(ctx, nt)
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	write := func(task *store.Task, name, body string) {
		t.Helper()
		if _, err := e.store.WriteTaskFile(ctx, task.ID, name, "", []byte(body), "a"); err != nil {
			t.Fatal(err)
		}
	}
	parent := newTask("Parent", nil)
	c1, c2 := newTask("Child 1", parent), newTask("Child 2", parent)
	grandchild, unrelated := newTask("Grandchild", c1), newTask("Unrelated", nil)
	write(parent, "x.md", "parent's x")
	write(c1, "x.md", "c1's x")
	write(c1, "design.md", "c1's design")
	write(c2, "design.md", "c2's design")
	write(grandchild, "deep.md", "too deep")
	write(unrelated, "other.md", "not yours")

	list, total, err := e.store.ListTaskFiles(ctx, parent.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range list {
		got = append(got, fmt.Sprintf("%s#%d:%v", f.Name, f.TaskNumber, f.Subtask))
	}
	want := []string{
		fmt.Sprintf("x.md#%d:false", parent.Number),
		fmt.Sprintf("design.md#%d:true", c1.Number), fmt.Sprintf("design.md#%d:true", c2.Number),
		fmt.Sprintf("x.md#%d:true", c1.Number),
	}
	if total != 4 || strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("list: %v (total %d), want %v", got, total, want)
	}
	if one, total, _ := e.store.ListTaskFiles(ctx, parent.ID, 2); len(one) != 2 || total != 4 || one[0].Subtask || !one[1].Subtask {
		t.Fatalf("limited list: %d of %d", len(one), total)
	}
	// c1's own files shadow the parent's x.md; its own subtask's file comes last.
	if list, _, _ := e.store.ListTaskFiles(ctx, c1.ID, 10); len(list) != 3 || list[0].Name != "design.md" || list[1].TaskID != c1.ID ||
		list[1].Subtask || list[2].Name != "deep.md" || !list[2].Subtask {
		t.Fatalf("c1's list: %v", list)
	}

	read := func(task *store.Task, owner uuid.UUID, name string) (string, error) {
		t.Helper()
		f, rc, err := e.store.OpenTaskFile(ctx, task.ID, owner, name)
		if err != nil {
			return "", err
		}
		defer rc.Close()
		b, _ := io.ReadAll(rc)
		if owner != uuid.Nil && f.TaskID != owner {
			t.Fatalf("read %s of %s, got the file of %s", name, owner, f.TaskID)
		}
		return string(b), nil
	}
	for _, c := range []struct {
		task  *store.Task
		owner uuid.UUID
		name  string
		body  string
		err   error
	}{
		{parent, uuid.Nil, "x.md", "parent's x", nil},           // both have it: the task's own
		{parent, uuid.Nil, "design.md", "", domain.ErrNotFound}, // only subtasks have it
		{parent, c1.ID, "x.md", "c1's x", nil},
		{parent, c1.ID, "design.md", "c1's design", nil},
		{parent, c2.ID, "design.md", "c2's design", nil},
		{parent, parent.ID, "x.md", "parent's x", nil},
		{parent, c2.ID, "x.md", "", domain.ErrNotFound},
		{c1, parent.ID, "x.md", "parent's x", nil}, // an ancestor by owner
		{parent, grandchild.ID, "deep.md", "", store.ErrTaskFileNotReadable},
		{parent, unrelated.ID, "other.md", "", store.ErrTaskFileNotReadable},
		{c2, c1.ID, "design.md", "", store.ErrTaskFileNotReadable}, // a sibling
		{parent, uuid.New(), "x.md", "", store.ErrTaskFileNotReadable},
	} {
		body, err := read(c.task, c.owner, c.name)
		if body != c.body || !errors.Is(err, c.err) || (err == nil) != (c.err == nil) {
			t.Errorf("task %d reads %s of %s: %q %v, want %q %v", c.task.Number, c.name, c.owner, body, err, c.body, c.err)
		}
	}
	if f, data, err := e.store.ReadTaskFileRange(ctx, parent.ID, c2.ID, "design.md", 5, 6); err != nil || string(data) != "design" || f.TaskID != c2.ID {
		t.Fatalf("range of a subtask's file: %q %v", data, err)
	}
	if _, _, err := e.store.ReadTaskFileRange(ctx, parent.ID, grandchild.ID, "deep.md", 0, 10); !errors.Is(err, store.ErrTaskFileNotReadable) {
		t.Fatalf("range of a grandchild's file: %v", err)
	}

	if nums, err := e.store.SubtasksWithFile(ctx, parent.ID, "design.md"); err != nil || fmt.Sprint(nums) != fmt.Sprint([]int64{c1.Number, c2.Number}) {
		t.Fatalf("subtasks with design.md: %v %v", nums, err)
	}
	if nums, err := e.store.SubtasksWithFile(ctx, parent.ID, "deep.md"); err != nil || len(nums) != 0 {
		t.Fatalf("a grandchild's file counted: %v %v", nums, err)
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

// failingDeletes is a blob backend whose deletes fail while fail is set. onDelete, when set, runs
// before each delete.
type failingDeletes struct {
	blob.Store
	fail     bool
	onDelete func()
}

func (f *failingDeletes) Delete(ctx context.Context, id string) error {
	if f.onDelete != nil {
		f.onDelete()
	}
	if f.fail {
		return errors.New("bucket unavailable")
	}
	return f.Store.Delete(ctx, id)
}

// Blobs are deleted once the rows' delete has committed, never inside its transaction: a blob that
// can't be deleted is left behind, the rows are gone anyway, and the error (ErrBlobsLeftBehind)
// names the file.
func TestDeleteTaskDropsBlobsAfterCommit(t *testing.T) {
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
	a, err := e.store.WriteTaskFile(ctx, task.ID, "a.txt", "", []byte("a"), "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.store.WriteTaskFile(ctx, task.ID, "b.txt", "", []byte("b"), "a")
	if err != nil {
		t.Fatal(err)
	}

	flaky.fail = true
	flaky.onDelete = func() {
		if _, _, err := readTaskFile(t, e, task, "a.txt"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("the file row was not yet deleted when its blob was: %v", err)
		}
	}
	err = e.store.DeleteTaskFile(ctx, task.ID, "a.txt")
	if !errors.Is(err, store.ErrBlobsLeftBehind) || !strings.Contains(err.Error(), a.ID.String()) {
		t.Fatalf("deleting a file whose blob could not be deleted: %v", err)
	}

	flaky.onDelete = func() {
		if _, err := e.store.GetTask(ctx, task.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("the task was not yet deleted when its blobs were: %v", err)
		}
	}
	err = e.store.DeleteTask(ctx, task.ID)
	if !errors.Is(err, store.ErrBlobsLeftBehind) || !strings.Contains(err.Error(), b.ID.String()) {
		t.Fatalf("deleting a task whose blob could not be deleted: %v", err)
	}
	if _, err := e.store.GetTask(ctx, task.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("task still there: %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 2 {
		t.Fatalf("%d blobs left, want the 2 that could not be deleted", len(left))
	}

	flaky.fail, flaky.onDelete = false, nil
	task, err = e.store.CreateTask(ctx, store.NewTask{Title: "t2"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.store.WriteTaskFile(ctx, task.ID, "c.txt", "", []byte("c"), "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, c.ID.String())); !os.IsNotExist(err) {
		t.Fatalf("blob of a deleted task still there: %v", err)
	}
}

// Through the API, a delete whose blobs could not be deleted is still a delete: the single endpoints
// answer 204 and send their event, and bulk lists the task as deleted and goes on with the rest.
func TestDeleteAPILeavesBlobsBehind(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	fs, err := blob.NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.store.SetBlobs(&failingDeletes{Store: fs, fail: true})
	tasks := make([]store.Task, 3)
	for i := range tasks {
		if code := e.do("POST", "/tasks", map[string]any{"title": fmt.Sprint("t", i), "status": "BACKLOG"}, &tasks[i]); code != 201 {
			t.Fatalf("create: %d", code)
		}
		if _, err := e.store.WriteTaskFile(ctx, tasks[i].ID, "a.txt", "", []byte("a"), "a"); err != nil {
			t.Fatal(err)
		}
	}
	events, unsubscribe := e.server.Broker.Subscribe()
	defer unsubscribe()
	expect := func(name string) {
		t.Helper()
		for {
			select {
			case ev := <-events:
				if ev.Name == name {
					return
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("no %s event", name)
			}
		}
	}
	gone := func(task store.Task) {
		t.Helper()
		if _, err := e.store.GetTask(ctx, task.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("task %d still there: %v", task.Number, err)
		}
	}

	if code := e.do("DELETE", "/tasks/"+tasks[0].ID.String()+"/files?name=a.txt", nil, nil); code != 204 {
		t.Fatalf("delete file: %d", code)
	}
	expect("task.file")
	if code := e.do("DELETE", "/tasks/"+tasks[0].ID.String()+"/files?name=a.txt", nil, nil); code != 404 {
		t.Fatalf("delete file again: %d", code)
	}
	if _, err := e.store.WriteTaskFile(ctx, tasks[0].ID, "b.txt", "", []byte("b"), "a"); err != nil {
		t.Fatal(err)
	}
	if code := e.do("DELETE", "/tasks/"+tasks[0].ID.String(), nil, nil); code != 204 {
		t.Fatalf("delete task: %d", code)
	}
	expect("task.deleted")
	gone(tasks[0])

	var res struct {
		OK     []uuid.UUID       `json:"ok"`
		Failed []json.RawMessage `json:"failed"`
	}
	ids := []string{tasks[1].ID.String(), tasks[2].ID.String()}
	if code := e.do("POST", "/tasks/bulk", map[string]any{"action": "delete", "ids": ids}, &res); code != 200 {
		t.Fatalf("bulk delete: %d", code)
	}
	if len(res.OK) != 2 || len(res.Failed) != 0 {
		t.Fatalf("bulk delete: ok %v, failed %s", res.OK, res.Failed)
	}
	expect("task.deleted")
	expect("task.deleted")
	gone(tasks[1])
	gone(tasks[2])
}
