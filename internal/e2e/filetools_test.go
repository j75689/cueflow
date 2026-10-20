package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

// An agent saves a file, pages through it with read_file, and gets tool errors (not a failed run) for
// a missing file and a read past the end. Its subtask sees the file in its brief and reads it; the
// subtask's own file is not visible to the parent.
func TestAgentFileTools(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	notes := "Plan for Disney+\n" + strings.Repeat("字", 35000)
	total := len([]rune(notes))

	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		b := brief(req)
		res := toolResults(req)
		switch whoAmI(req.System) {
		case "chief":
			if strings.Contains(b, "## Subtasks") {
				if !strings.Contains(b, "## Files") || !strings.Contains(b, "- notes.md (") || strings.Contains(b, "results.md") {
					t.Errorf("the resumed chief should see its own file and not its subtask's:\n%s", b)
				}
				return calls("complete_task", map[string]any{"summary": "done"})
			}
			switch len(res) {
			case 0:
				return calls("write_file", map[string]any{"name": "notes.md", "content": notes})
			case 1:
				if res[0].IsError || !strings.Contains(res[0].Content, fmt.Sprintf("Saved notes.md (%d characters)", total)) {
					t.Errorf("write_file: %+v", res[0])
				}
				return calls("read_file", map[string]any{"name": "notes.md"},
					"read_file", map[string]any{"name": "notes.md", "offset": 30000, "limit": 10},
					"read_file", map[string]any{"name": "missing.md"},
					"read_file", map[string]any{"name": "notes.md", "offset": total + 5})
			case 5:
				first, page, missing, past := res[1], res[2], res[3], res[4]
				if first.IsError || !strings.Contains(first.Content, fmt.Sprintf("characters 0–30000 of %d", total)) ||
					!strings.Contains(first.Content, "offset 30000 for the rest") || !strings.Contains(first.Content, "Plan for Disney+") ||
					strings.Contains(first.Content, "truncated") {
					t.Errorf("first page: %.200s", first.Content)
				}
				if page.IsError || !strings.HasSuffix(page.Content, strings.Repeat("字", 10)) ||
					!strings.Contains(page.Content, fmt.Sprintf("characters 30000–30010 of %d", total)) {
					t.Errorf("second page: %.200s", page.Content)
				}
				if !missing.IsError || !strings.Contains(missing.Content, `no file named "missing.md"`) || !strings.Contains(missing.Content, "notes.md") {
					t.Errorf("missing file should be a tool error naming what exists: %+v", missing)
				}
				if !past.IsError || !strings.Contains(past.Content, "past the end") {
					t.Errorf("a read past the end should be a tool error: %+v", past)
				}
				return calls("create_subtask", map[string]any{"title": "Read the plan", "description": "Use notes.md", "assignee": "researcher"},
					"wait_for_subtasks", map[string]any{})
			}
		case "researcher":
			if len(res) == 0 {
				if !strings.Contains(b, "## Files") || !strings.Contains(b, "- notes.md (") || !strings.Contains(b, "from task #") ||
					!strings.Contains(b, ": Plan for Disney+") || strings.Contains(b, strings.Repeat("字", 200)) {
					t.Errorf("the subtask brief should list the parent's file without its contents:\n%s", b)
				}
				return calls("read_file", map[string]any{"name": "notes.md", "limit": 4},
					"write_file", map[string]any{"name": "results.md", "content": "Findings"})
			}
			if res[0].IsError || !strings.Contains(res[0].Content, "(from task #") || !strings.Contains(res[0].Content, "Plan") {
				t.Errorf("the subtask should read its parent's file: %+v", res[0])
			}
			return calls("complete_task", map[string]any{"summary": "read it"})
		}
		t.Errorf("unexpected turn for %s with %d results", whoAmI(req.System), len(res))
		return calls("complete_task", map[string]any{"summary": "?"})
	})

	var task store.Task
	if code := e.do("POST", "/tasks", map[string]any{"title": "Plan Disney+ support", "assignee_agent_id": team["chief"].ID}, &task); code != 201 {
		t.Fatalf("create task: %d", code)
	}
	e.crew.RunPending(ctx, 10)
	got, err := e.store.GetTask(ctx, task.ID)
	if err != nil || got.Status != store.TaskReview {
		t.Fatalf("the task should finish in REVIEW: %v %v", got.Status, err)
	}
	if files, _ := e.store.ListTaskFiles(ctx, task.ID); len(files) != 1 || files[0].Name != "notes.md" || files[0].CreatedBy != "chief" {
		t.Fatalf("the chief's files: %+v", files)
	}
	subs, err := e.store.ListTasks(ctx, store.TaskFilter{ParentID: &task.ID, All: true})
	if err != nil || len(subs) != 1 {
		t.Fatalf("subtasks: %v %v", subs, err)
	}
	files, _ := e.store.ListTaskFiles(ctx, subs[0].ID)
	names := map[string]string{}
	for _, f := range files {
		names[f.Name] = f.CreatedBy
	}
	if len(files) != 2 || names["results.md"] != "researcher" || names["notes.md"] != "chief" {
		t.Fatalf("the subtask should see its own results.md and its parent's notes.md: %v", names)
	}
}

// The brief lists at most 30 files and says how many more there are.
func TestBriefFilesCapped(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	var task store.Task
	if code := e.do("POST", "/tasks", map[string]any{"title": "Many files", "assignee_agent_id": team["writer"].ID}, &task); code != 201 {
		t.Fatalf("create task: %d", code)
	}
	for i := range 35 {
		if _, err := e.store.WriteTaskFile(ctx, task.ID, fmt.Sprintf("f%02d.txt", i), "", []byte("line"), "human"); err != nil {
			t.Fatal(err)
		}
	}
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		b := brief(req)
		if strings.Count(b, "\n- f") != 30 || !strings.Contains(b, "(5 more; call list_files)") {
			t.Errorf("brief files:\n%s", b)
		}
		return calls("complete_task", map[string]any{"summary": "ok"})
	})
	e.crew.RunPending(ctx, 10)
}
