package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

// dump_text is a test tool that returns text of the size asked for, or a non-text result.
func init() {
	crew.RegisterTool(crew.ToolSpec{Name: "dump_text", Description: "Test tool: returns text of the given size.",
		Schema: crew.Obj(map[string]any{"chars": crew.Integer("How many characters"), "binary": map[string]any{"type": "boolean"}}),
		Run: func(_ context.Context, _ *crew.Run, raw json.RawMessage) (string, error) {
			var a struct {
				Chars  int
				Binary bool
			}
			_ = crew.DecodeArgs(raw, &a)
			if a.Binary {
				return "\xff\xfe" + strings.Repeat("b", a.Chars), nil
			}
			return "line one\n" + strings.Repeat("字", a.Chars-9), nil
		}})
}

// A result over the threshold is saved as a file of the task before the model sees it; the model gets
// where it is, its size and its start, and can read the rest. Short and non-text results stay inline.
// A file the agent already wrote under the next name is not overwritten.
func TestLargeToolResultsSavedAsFiles(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	var task store.Task
	if code := e.do("POST", "/tasks", map[string]any{"title": "Dump", "assignee_agent_id": team["writer"].ID}, &task); code != 201 {
		t.Fatalf("create task: %d", code)
	}
	// The agent's own file sits where the first saved result would go.
	if _, err := e.store.WriteTaskFile(ctx, task.ID, "results/dump_text-1.txt", "", []byte("mine"), "writer"); err != nil {
		t.Fatal(err)
	}
	const big = 20000
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		res := toolResults(req)
		switch len(res) {
		case 0:
			return calls("dump_text", map[string]any{"chars": big}, "dump_text", map[string]any{"chars": 500},
				"dump_text", map[string]any{"chars": big, "binary": true})
		case 3:
			saved, short, binary := res[0].Content, res[1].Content, res[2].Content
			if !strings.Contains(saved, fmt.Sprintf("dump_text returned %d characters, saved as the file results/dump_text-2.txt", big)) ||
				!strings.Contains(saved, "line one") || strings.Count(saved, "字") != 2000-9 {
				t.Errorf("saved stub: %.300s", saved)
			}
			if strings.Contains(short, "saved as") || strings.Count(short, "字") != 491 {
				t.Errorf("a short result should stay inline: %.200s", short)
			}
			if strings.Contains(binary, "saved as") {
				t.Errorf("a non-text result should stay inline")
			}
			// The offset in the stub continues right after what the model saw.
			i := strings.Index(saved, "offset ")
			var off int
			fmt.Sscanf(saved[i+len("offset "):], "%d", &off)
			return calls("read_file", map[string]any{"name": "results/dump_text-2.txt", "offset": off, "limit": 5})
		case 4:
			if page := res[3].Content; res[3].IsError || !strings.HasSuffix(page, "\n\n"+strings.Repeat("字", 5)) ||
				!strings.Contains(page, fmt.Sprintf("of %d", big+len([]rune("# Result of dump_text {\"chars\":20000}\n\n")))) {
				t.Errorf("reading on from the stub's offset: %.300s", page)
			}
			return calls("complete_task", map[string]any{"summary": "ok"})
		}
		t.Errorf("unexpected turn with %d results", len(res))
		return calls("complete_task", map[string]any{"summary": "?"})
	})
	e.crew.RunPending(ctx, 10)
	if got, err := e.store.GetTask(ctx, task.ID); err != nil || got.Status != store.TaskReview {
		t.Fatalf("the run should have finished: %v %v", got.Status, err)
	}

	files, _, _ := e.store.ListTaskFiles(ctx, task.ID, 10)
	if len(files) != 2 || files[0].Name != "results/dump_text-1.txt" || files[1].Name != "results/dump_text-2.txt" {
		t.Fatalf("files: %+v", files)
	}
	if string(files[0].Head) != "mine" {
		t.Fatal("the agent's own file was overwritten")
	}
	// The first line, which the brief's Files list shows, names the tool and its arguments.
	if !strings.HasPrefix(string(files[1].Head), `# Result of dump_text {"chars":20000}`) || files[1].CreatedBy != "writer" {
		t.Fatalf("saved file head: %q by %s", files[1].Head, files[1].CreatedBy)
	}
	// The thread records which file the result went to.
	var msgs []store.TaskMessage
	e.do("GET", "/tasks/"+task.ID.String()+"/messages", nil, &msgs)
	found := false
	for _, m := range msgs {
		if m.Kind == "tool_result" && strings.Contains(string(m.Data), `"file":"results/dump_text-2.txt"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("the thread doesn't say where the result was saved")
	}
}
