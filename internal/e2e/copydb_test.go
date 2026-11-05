package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/deliverable"
	deliverablesqlite "github.com/j75689/cueflow/internal/deliverable/sqlite"
	"github.com/j75689/cueflow/internal/store"
	storesqlite "github.com/j75689/cueflow/internal/store/sqlite"
)

// copyDBTables are the tables cueflow copy-db copies: SQLite's baseline.
var copyDBTables = []string{"connectors", "orgs", "agents", "agent_memories", "schedules", "tasks", "task_messages",
	"agent_runs", "agent_tokens", "run_tokens", "tool_approvals", "media", "clock", "runners", "runner_registrations",
	"runner_jobs", "task_files", "deliverables", "deliverable_audit_logs", "source_items"}

// cueflow copy-db copies a PostgreSQL install into a new SQLite file the SQLite backend then serves
// the same tasks, agents and deliverables from; it refuses a target with data in it and a source at
// another migration before writing anything.
func TestCopyDB(t *testing.T) {
	e := setup(t)
	pool := pgOnly(t, e)
	ctx := context.Background()
	bin := buildCueflow(t)
	from := os.Getenv("CUEFLOW_TEST_DATABASE_URL")

	// Seed through the product: connectors, media and a deliverable using it ("\/media\/<id>", as a
	// client may send it), agents (one inactive), tasks with and without a parent, labels and a due
	// date, messages, a memory, a task file, an agent token and a finished run.
	e.do("POST", "/connectors", map[string]any{"name": "Img", "type": "fake_image", "config": map[string]any{}}, nil)
	var m mediaOut
	if code := e.do("POST", "/media/generate", map[string]any{"prompt": "a"}, &m); code != 201 {
		t.Fatalf("generate: %d", code)
	}
	payload := `{"content":"R&D notes, see \/media\/` + m.ID + `","n":12345678901234567890,"tags":["é","<b>"]}`
	if code := e.do("POST", "/deliverables", map[string]any{"kind": "document", "title": "notes", "payload": json.RawMessage(payload)}, nil); code != 201 {
		t.Fatalf("create deliverable: %d", code)
	}
	def, _ := e.store.DefaultOrg(ctx)
	writer, err := e.store.CreateAgent(ctx, &store.Agent{OrgID: def.ID, Name: "Writer", Slug: "writer", Capabilities: []string{"research", "memory"}, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.CreateAgent(ctx, &store.Agent{OrgID: def.ID, Name: "Idle", Slug: "idle"}); err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(48 * time.Hour)
	parent, err := e.store.CreateTask(ctx, store.NewTask{Title: "Parent", Labels: []string{"a", "b"}, AssigneeAgentID: &writer.ID, DueAt: &due})
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.store.CreateTask(ctx, store.NewTask{Title: "Child", ParentID: &parent.ID, Status: store.TaskTodo})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.AddTaskMessage(ctx, child.ID, "human", nil, "message", "hello", map[string]any{"k": []int{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.AddMemory(ctx, writer.ID, "remember this"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.WriteTaskFile(ctx, parent.ID, "notes.md", "text/markdown", []byte("# notes"), "agent"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.CreateAgentToken(ctx, writer.ID, "ci"); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Insert(ctx, "agent_runs", map[string]any{"id": uuid.New(), "task_id": parent.ID, "agent_id": writer.ID, "org_id": def.ID,
		"status": "DONE", "turns": 3, "input_tokens": 100, "output_tokens": 50, "started_at": time.Now().Add(-time.Minute), "finished_at": time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Rewriting the parent last moves its row after the child's, so the copy reads the subtask first.
	if _, err := e.db.Update(ctx, "tasks", map[string]any{"description": "the parent"}, map[string]any{"id": parent.ID}); err != nil {
		t.Fatal(err)
	}
	var first string
	if err := pool.QueryRow(ctx, `SELECT title FROM tasks LIMIT 1`).Scan(&first); err != nil || first != "Child" {
		t.Fatalf("PostgreSQL reads %q first (%v), want the subtask", first, err)
	}

	dir := t.TempDir()
	copyPath := filepath.Join(dir, "copy.db")
	to := "sqlite://" + copyPath
	out, err := exec.Command(bin, "copy-db", "--from", from, "--to", to).CombinedOutput()
	if err != nil {
		t.Fatalf("copy-db: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "row counts match") {
		t.Errorf("output: %s", out)
	}
	for _, skipped := range []string{"audit_logs", "pipeline_runs", "pipelines", "posts"} {
		if !strings.Contains(string(out), skipped+" (0 row(s))") {
			t.Errorf("output doesn't name %s as not copied: %s", skipped, out)
		}
	}

	lite, err := storesqlite.Open(ctx, to, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := storesqlite.Migrate(ctx, lite); err != nil {
		t.Fatalf("migrate the copy: %v", err)
	}
	liteDS := deliverablesqlite.New(lite)
	liteDB := &storesqlite.TestHooks{Store: lite}
	for _, table := range copyDBTables {
		want, err := e.db.Count(ctx, table, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := liteDB.Count(ctx, table, nil)
		if err != nil || got != want {
			t.Errorf("%s: %d rows in the copy, %d in postgres (%v)", table, got, want, err)
		}
	}

	pgTasks, err := e.store.ListTasks(ctx, store.TaskFilter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	liteTasks, err := lite.ListTasks(ctx, store.TaskFilter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "tasks", pgTasks, liteTasks)
	pgAgents, err := e.store.ListAgents(ctx, def.ID)
	if err != nil {
		t.Fatal(err)
	}
	liteAgents, err := lite.ListAgents(ctx, def.ID)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "agents", pgAgents, liteAgents)
	pgItems, _, err := e.ds.List(ctx, deliverable.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	liteItems, _, err := liteDS.List(ctx, deliverable.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "deliverables", pgItems, liteItems)
	if len(pgTasks) != 2 || len(pgAgents) != 2 || len(pgItems) != 1 {
		t.Fatalf("seeded %d tasks, %d agents, %d deliverables", len(pgTasks), len(pgAgents), len(pgItems))
	}

	// What the stores read back by id: the parent link, the due date (NULL on the child), the file.
	got, err := lite.GetTask(ctx, child.ID)
	if err != nil || got.ParentID == nil || *got.ParentID != parent.ID || got.DueAt != nil {
		t.Errorf("child in the copy: %+v %v", got, err)
	}
	if got, err := lite.GetTask(ctx, parent.ID); err != nil || got.DueAt == nil || !got.DueAt.Equal(due.Truncate(time.Microsecond)) {
		t.Errorf("parent due date in the copy: %+v %v", got, err)
	}
	if files, _, err := lite.ListTaskFiles(ctx, parent.ID, 10); err != nil || len(files) != 1 || files[0].Name != "notes.md" || string(files[0].Head) != "# notes" {
		t.Errorf("task files in the copy: %+v %v", files, err)
	}
	// The payload is stored as the SQLite store stores it, so the in-use check finds the media.
	mediaID := uuid.MustParse(m.ID)
	if used, err := liteDS.MediaInUse(ctx, mediaID); err != nil || !used {
		t.Errorf("media in use in the copy: %v %v", used, err)
	}
	var stored string
	want := `{"content":"R&D notes, see /media/` + m.ID + `","n":12345678901234567890,"tags":["é","<b>"]}`
	if err := liteDB.Get(ctx, "deliverables", "payload", map[string]any{"id": pgItems[0].ID}, &stored); err != nil || stored != want {
		t.Errorf("stored payload:\n%s %v\nwant\n%s", stored, err, want)
	}

	// Refusals exit non-zero and leave the target as it was: the same bytes in the file, its -wal and
	// its -shm, or still no file.
	refuseAt := func(name, path, to, want string) {
		t.Helper()
		before := fileHashes(t, path)
		out, err := exec.Command(bin, "copy-db", "--from", from, "--to", to).CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() == 0 || !strings.Contains(string(out), want) {
			t.Errorf("%s: %v, want a non-zero exit saying %q:\n%s", name, err, want, out)
		}
		if after := fileHashes(t, path); !reflect.DeepEqual(after, before) {
			t.Errorf("%s: the target changed:\nbefore %v\nafter  %v", name, before, after)
		}
	}
	refuse := func(name, path, want string) {
		t.Helper()
		refuseAt(name, path, "sqlite://"+path, want)
	}
	lite.Close()
	refuse("into the copy", copyPath, "target is not empty (connectors has 1 row(s))")

	stale := filepath.Join(dir, "stale.db")
	staleDB, err := storesqlite.Open(ctx, "sqlite://"+stale, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := storesqlite.Migrate(ctx, staleDB); err == nil {
		_, err = (&storesqlite.TestHooks{Store: staleDB}).Update(ctx, "schema_migrations", map[string]any{"version": "0026_older"}, nil)
	}
	staleDB.Close()
	if err != nil {
		t.Fatal(err)
	}
	refuse("into a target at another migration", stale, "target at 0026_older")

	// A SQLite file of something else, in rollback-journal mode: opening it as the store would
	// switch it to WAL.
	foreign := filepath.Join(dir, "foreign.db")
	foreignDB, err := storesqlite.Open(ctx, "sqlite://"+foreign, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`CREATE TABLE notes (body TEXT)`, `INSERT INTO notes VALUES ('keep')`} {
		if _, err := foreignDB.Writer().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	foreignDB.Close()
	// Header bytes 18 and 19 are 2 in WAL mode and 1 in rollback-journal mode (sqlite.org/fileformat.html).
	header, err := os.ReadFile(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if header[18] != 2 || header[19] != 2 {
		t.Fatalf("foreign file header bytes 18-19: %v, want WAL mode", header[18:20])
	}
	header[18], header[19] = 1, 1
	if err := os.WriteFile(foreign, header, 0o600); err != nil {
		t.Fatal(err)
	}
	refuse("into a foreign SQLite file", foreign, "not a cueflow database")

	text := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(text, []byte("not a database, but long enough to be read as one"), 0o600); err != nil {
		t.Fatal(err)
	}
	refuse("into a text file", text, "file is not a database")

	// A target with data named the way SQLite reads it: percent-encoded, with a localhost authority.
	spaced := filepath.Join(dir, "sp ace", "live.db")
	if err := os.Mkdir(filepath.Dir(spaced), 0o755); err != nil {
		t.Fatal(err)
	}
	liveDB, err := storesqlite.Open(ctx, "sqlite://"+spaced, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := storesqlite.Migrate(ctx, liveDB); err == nil {
		err = (&storesqlite.TestHooks{Store: liveDB}).Insert(ctx, "orgs", map[string]any{"id": uuid.New(), "name": "Mine", "slug": "mine"})
	}
	liveDB.Close()
	if err != nil {
		t.Fatal(err)
	}
	enc := strings.ReplaceAll(spaced, " ", "%20")
	for _, to := range []string{"file:" + enc, "sqlite://" + enc, "file://localhost" + enc} {
		refuseAt("into a target with data at "+to, spaced, to, "target is not empty (orgs has 1 row(s))")
	}

	// A copy that fails after its first write says the target is incomplete and which files to delete.
	if _, err := pool.Exec(ctx, `UPDATE tasks SET position = 'NaN' WHERE id = $1`, child.ID); err != nil {
		t.Fatal(err)
	}
	half := filepath.Join(dir, "half.db")
	out, err = exec.Command(bin, "copy-db", "--from", from, "--to", "sqlite://"+half).CombinedOutput()
	incomplete := fmt.Sprintf("the copy in %[1]s is incomplete: delete %[1]s, %[1]s-wal and %[1]s-shm before copying again", half)
	if err == nil || !strings.Contains(string(out), "copy tasks:") || !strings.Contains(string(out), incomplete) {
		t.Errorf("failed copy: %v, want copy tasks failing with %q:\n%s", err, incomplete, out)
	}

	// A source at another migration than this cueflow's, ahead or behind: refused before the target
	// is created or touched.
	fresh := filepath.Join(dir, "fresh.db")
	empty := filepath.Join(dir, "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ('0028_future')`); err != nil {
		t.Fatal(err)
	}
	refuse("from a source ahead into a new file", fresh, "source is at migration 0028_future")
	refuse("from a source ahead into an empty file", empty, "source is at migration 0028_future")
	refuse("from a source ahead into the copy", copyPath, "source is at migration 0028_future")
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version >= '0027'`); err != nil {
		t.Fatal(err)
	}
	refuse("from a source behind into a new file", fresh, "source is at migration 0026_")
	refuse("from a source behind into an empty file", empty, "source is at migration 0026_")
	refuse("from a source behind into a foreign SQLite file", foreign, "source is at migration 0026_")
}

// fileHashes is the sha256 of the SQLite file path and of its -wal and -shm, "absent" for a missing
// one.
func fileHashes(t *testing.T, path string) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		b, err := os.ReadFile(name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			hashes[name] = "absent"
		case err != nil:
			t.Fatal(err)
		default:
			hashes[name] = fmt.Sprintf("%x", sha256.Sum256(b))
		}
	}
	return hashes
}

// buildCueflow builds the cueflow command into t's temporary directory.
func buildCueflow(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cueflow")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/j75689/cueflow/cmd/cueflow").CombinedOutput(); err != nil {
		t.Fatalf("build cueflow: %v\n%s", err, out)
	}
	return bin
}

// sameJSON fails t unless a and b encode to the same JSON, ignoring the order of the list and of
// object keys and the zone times are written in.
func sameJSON(t *testing.T, what string, a, b any) {
	t.Helper()
	norm := func(v any) any {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var out []any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].(map[string]any)["id"].(string) < out[j].(map[string]any)["id"].(string)
		})
		return utcTimes(out)
	}
	if na, nb := norm(a), norm(b); !reflect.DeepEqual(na, nb) {
		ja, _ := json.MarshalIndent(na, "", " ")
		jb, _ := json.MarshalIndent(nb, "", " ")
		t.Errorf("%s differ:\npostgres: %s\nsqlite:   %s", what, ja, jb)
	}
}

// utcTimes rewrites the RFC 3339 times in v in UTC.
func utcTimes(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			v[k] = utcTimes(x)
		}
	case []any:
		for i, x := range v {
			v[i] = utcTimes(x)
		}
	case string:
		if ts, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return ts.UTC().Format(time.RFC3339Nano)
		}
	}
	return v
}
