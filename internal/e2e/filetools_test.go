package e2e

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

// An agent saves a file, pages through it with read_file, and gets tool errors (not a failed run) for
// a missing file and a read past the end. Its subtask sees the file in its brief and reads it. Back
// in the parent, the subtask's files are listed last and read only by naming the subtask: a bare name
// still resolves to the parent's own file, and a task that is not a direct subtask (or is in another
// org) is refused.
func TestAgentFileTools(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	notes := "Plan for Disney+\n" + strings.Repeat("字", 35000)
	total := len([]rune(notes))

	// A task of the same org that is not the chief's subtask, and one of another org whose number the
	// default org doesn't have; both have a file the chief must not read.
	unrelated, err := e.store.CreateTask(ctx, store.NewTask{Title: "Unrelated", Status: store.TaskBacklog})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.WriteTaskFile(ctx, unrelated.ID, "results.md", "", []byte("not yours"), "human"); err != nil {
		t.Fatal(err)
	}
	other, err := e.store.CreateOrg(ctx, &store.Org{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	var foreign *store.Task
	for range 10 {
		if foreign, err = e.store.CreateTask(ctx, store.NewTask{OrgID: other.ID, Title: "Foreign", Status: store.TaskBacklog}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.store.WriteTaskFile(ctx, foreign.ID, "results.md", "", []byte("another org's"), "human"); err != nil {
		t.Fatal(err)
	}
	subNumber := regexp.MustCompile(`results\.md \([^)]*, from subtask #(\d+)\)`)

	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		b := brief(req)
		res := toolResults(req)
		switch whoAmI(req.System) {
		case "chief":
			if strings.Contains(b, "## Subtasks") {
				m := subNumber.FindStringSubmatch(b)
				if m == nil || !strings.Contains(b, "- notes.md (") || strings.Index(b, "- notes.md (") > strings.Index(b, "- results.md (") {
					t.Errorf("the resumed chief should see its own file, then its subtask's:\n%s", b)
					return calls("complete_task", map[string]any{"summary": "done"})
				}
				sub, _ := strconv.Atoi(m[1])
				if len(res) == 0 {
					return calls("read_file", map[string]any{"name": "results.md"},
						"read_file", map[string]any{"name": "results.md", "task": sub},
						"read_file", map[string]any{"name": "notes.md", "limit": 16},
						"read_file", map[string]any{"name": "notes.md", "task": sub},
						"read_file", map[string]any{"name": "results.md", "task": unrelated.Number},
						"read_file", map[string]any{"name": "results.md", "task": foreign.Number},
						"list_files", map[string]any{})
				}
				bare, byTask, own, subsNotes, notChild, otherOrg, list := res[0], res[1], res[2], res[3], res[4], res[5], res[6]
				if !bare.IsError || !strings.Contains(bare.Content, "subtask #"+m[1]) || !strings.Contains(bare.Content, "task set to") {
					t.Errorf("a bare name of a subtask's file should name the subtask and the task argument: %+v", bare)
				}
				if byTask.IsError || !strings.Contains(byTask.Content, "(from subtask #"+m[1]+")") || !strings.Contains(byTask.Content, "Findings") {
					t.Errorf("read_file with task should read the subtask's file: %+v", byTask)
				}
				if own.IsError || !strings.Contains(own.Content, "Plan for Disney+") || strings.Contains(own.Content, "from") {
					t.Errorf("a bare name both have should be the chief's own file: %+v", own)
				}
				if subsNotes.IsError || !strings.Contains(subsNotes.Content, "my own notes") {
					t.Errorf("the subtask's file of the same name should be readable with task: %+v", subsNotes)
				}
				if !notChild.IsError || !strings.Contains(notChild.Content, "nothing was read") || strings.Contains(notChild.Content, "not yours") {
					t.Errorf("a task that is not a direct subtask should be refused: %+v", notChild)
				}
				if !otherOrg.IsError || !strings.Contains(otherOrg.Content, fmt.Sprintf("there is no task #%d", foreign.Number)) {
					t.Errorf("a task of another org should not be found: %+v", otherOrg)
				}
				if list.IsError || !strings.Contains(list.Content, "- notes.md (") || !strings.Contains(list.Content, ", from subtask #"+m[1]+"): my own notes") ||
					strings.Count(list.Content, "\n- ") != 2 { // notes.md, notes.md (subtask's), results.md
					t.Errorf("list_files should show the subtask's files last: %q", list.Content)
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
					"read_file", map[string]any{"name": "notes.md", "offset": total + 5},
					"read_file", map[string]any{"name": "notes.md", "offset": "30000"})
			case 6:
				first, page, missing, past, badArg := res[1], res[2], res[3], res[4], res[5]
				if !badArg.IsError || strings.Contains(badArg.Content, "name is required") || !strings.Contains(badArg.Content, "offset") {
					t.Errorf("a malformed argument should say what is wrong: %+v", badArg)
				}
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
					"write_file", map[string]any{"name": "results.md", "content": "Findings"},
					"write_file", map[string]any{"name": "notes.md", "content": "my own notes"})
			}
			if res[0].IsError || !strings.Contains(res[0].Content, "(from task #") || !strings.Contains(res[0].Content, "Plan") {
				t.Errorf("the subtask should read its parent's file: %+v", res[0])
			}
			if strings.Contains(res[1].Content, "same name") || !strings.Contains(res[2].Content, "has a file of the same name") {
				t.Errorf("write_file should say when it shadows an ancestor's file, and only then: %q / %q", res[1].Content, res[2].Content)
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
	// The chief's own file, then its subtask's two.
	if files, _, _ := e.store.ListTaskFiles(ctx, task.ID, 10); len(files) != 3 || files[0].Name != "notes.md" || files[0].CreatedBy != "chief" ||
		files[0].Subtask || !files[1].Subtask || !files[2].Subtask {
		t.Fatalf("the chief's files: %+v", files)
	}
	// The thread logs write_file's size and the start of its content, not the whole file.
	var msgs []store.TaskMessage
	e.do("GET", "/tasks/"+task.ID.String()+"/messages", nil, &msgs)
	logged := false
	for _, m := range msgs {
		if raw := string(m.Data); m.Kind == "tool_call" && strings.Contains(raw, "write_file") {
			logged = true
			if !strings.Contains(raw, fmt.Sprintf(`"characters": %d`, total)) && !strings.Contains(raw, fmt.Sprintf(`"characters":%d`, total)) ||
				strings.Count(raw, "字") > 2000 {
				t.Errorf("write_file logged %d characters of content", strings.Count(raw, "字"))
			}
		}
	}
	if !logged {
		t.Fatal("write_file call not in the thread")
	}
	subs, err := e.store.ListTasks(ctx, store.TaskFilter{ParentID: &task.ID, All: true})
	if err != nil || len(subs) != 1 {
		t.Fatalf("subtasks: %v %v", subs, err)
	}
	files, _, _ := e.store.ListTaskFiles(ctx, subs[0].ID, 10)
	names := map[string]string{}
	for _, f := range files {
		names[f.Name] = f.CreatedBy
	}
	// The subtask's notes.md now shadows its parent's, which is unchanged.
	if len(files) != 2 || names["results.md"] != "researcher" || names["notes.md"] != "researcher" {
		t.Fatalf("the subtask should see its own two files: %v", names)
	}
	if _, rc, err := e.store.OpenTaskFile(ctx, task.ID, uuid.Nil, "notes.md"); err != nil {
		t.Fatal(err)
	} else {
		b, _ := io.ReadAll(rc)
		rc.Close()
		if string(b) != notes {
			t.Fatal("the parent's notes.md changed")
		}
	}
}

// The brief lists at most 30 files and list_files at most 100, each saying how many more there are.
func TestBriefFilesCapped(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	var task store.Task
	if code := e.do("POST", "/tasks", map[string]any{"title": "Many files", "assignee_agent_id": team["writer"].ID}, &task); code != 201 {
		t.Fatalf("create task: %d", code)
	}
	for i := range 105 {
		if _, err := e.store.WriteTaskFile(ctx, task.ID, fmt.Sprintf("f%03d.txt", i), "", []byte("line"), "human"); err != nil {
			t.Fatal(err)
		}
	}
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		res := toolResults(req)
		if len(res) == 0 {
			b := brief(req)
			if strings.Count(b, "\n- f") != 30 || !strings.Contains(b, "(75 more; call list_files)") {
				t.Errorf("brief files:\n%s", b)
			}
			return calls("list_files", map[string]any{})
		}
		if strings.Count(res[0].Content, "- f") != 100 || !strings.Contains(res[0].Content, "(5 more not listed)") {
			t.Errorf("list_files should cap at 100 and count the rest: %d lines", strings.Count(res[0].Content, "- f"))
		}
		return calls("complete_task", map[string]any{"summary": "ok"})
	})
	e.crew.RunPending(ctx, 10)
}

// A direct subtask's files come after the task's own in the brief and in list_files, inside the same
// caps, even where their names sort first.
func TestBriefSubtaskFilesLast(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	var task store.Task
	if code := e.do("POST", "/tasks", map[string]any{"title": "Many files", "assignee_agent_id": team["writer"].ID}, &task); code != 201 {
		t.Fatalf("create task: %d", code)
	}
	child, err := e.store.CreateTask(ctx, store.NewTask{Title: "Child", Status: store.TaskBacklog, ParentID: &task.ID, CreatedBy: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 25 {
		if _, err := e.store.WriteTaskFile(ctx, task.ID, fmt.Sprintf("z%02d.txt", i), "", []byte("own"), "human"); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 10 {
		if _, err := e.store.WriteTaskFile(ctx, child.ID, fmt.Sprintf("a%02d.txt", i), "", []byte("child's"), "human"); err != nil {
			t.Fatal(err)
		}
	}
	fromChild := fmt.Sprintf(", from subtask #%d)", child.Number)
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		res := toolResults(req)
		if len(res) == 0 {
			b := brief(req)
			if strings.Count(b, "\n- z") != 25 || strings.Count(b, fromChild) != 5 || !strings.Contains(b, "- a04.txt (") ||
				strings.Contains(b, "- a05.txt") || strings.Index(b, "- a00.txt") < strings.Index(b, "- z24.txt") ||
				!strings.Contains(b, "(5 more; call list_files)") {
				t.Errorf("brief files:\n%s", b)
			}
			return calls("list_files", map[string]any{})
		}
		l := res[0].Content
		if strings.Count(l, "- z") != 25 || strings.Count(l, fromChild) != 10 || strings.Index(l, "- a00.txt") < strings.Index(l, "- z24.txt") ||
			strings.Contains(l, "more not listed") {
			t.Errorf("list_files should list the subtask's files after the task's own: %q", l)
		}
		return calls("complete_task", map[string]any{"summary": "ok"})
	})
	e.crew.RunPending(ctx, 10)
}
