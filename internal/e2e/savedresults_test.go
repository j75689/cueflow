package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/j75689/cueflow/internal/crew"
	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

// dump_text and dump_state are test tools behind their own capability, so only the agents of these
// tests have them. dump_text returns outside content (saved when long); dump_state stands for a tool
// that reports CueFlow's own state (never saved).
const dumpCapability = "test_dump"

func init() {
	crew.RegisterCapability(crew.Capability{ID: dumpCapability, Name: "Test dumps"})
	dump := func(_ context.Context, _ *crew.Run, raw json.RawMessage) (string, error) {
		var a struct {
			Chars  int
			Binary bool
		}
		_ = crew.DecodeArgs(raw, &a)
		if a.Binary {
			return "\xff\xfe" + strings.Repeat("b", a.Chars), nil
		}
		return "line one\n" + strings.Repeat("字", max(a.Chars-9, 0)), nil
	}
	schema := crew.Obj(map[string]any{"chars": crew.Integer("How many characters"), "binary": map[string]any{"type": "boolean"}})
	crew.RegisterTool(crew.ToolSpec{Name: "dump_text", Capability: dumpCapability, Description: "Test tool: returns text of the given size.",
		Schema: schema, Run: dump, SaveLargeResults: true})
	crew.RegisterTool(crew.ToolSpec{Name: "dump_state", Capability: dumpCapability, Description: "Test tool: a long state report.",
		Schema: schema, Run: dump})
}

// A result over the threshold is saved as a file of the task before the model sees it; the model gets
// where it is, its size and its start, and can read the rest. Short and non-text results stay inline, as does a long result of a tool that reports CueFlow's own state.
// A file the agent already wrote under the next name is not overwritten.
func TestLargeToolResultsSavedAsFiles(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	if code := e.do("PATCH", "/agents/"+team["writer"].ID.String(), map[string]any{"capabilities": append(team["writer"].Capabilities, dumpCapability)}, nil); code != 200 {
		t.Fatalf("give the writer the test tools: %d", code)
	}
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
				"dump_text", map[string]any{"chars": big, "binary": true}, "dump_state", map[string]any{"chars": big})
		case 4:
			saved, short, binary, state := res[0].Content, res[1].Content, res[2].Content, res[3].Content
			if strings.Contains(state, "saved as") || strings.Count(state, "字") != big-9 {
				t.Errorf("a state tool's result should stay inline: %.200s", state)
			}
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
		case 5:
			if page := res[4].Content; res[4].IsError || !strings.HasSuffix(page, "\n\n"+strings.Repeat("字", 5)) ||
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

// A long result of an approved MCP call is saved under the name the agent called the tool by (so it
// shares the sequence of direct calls), and the approval records the file.
func TestApprovedCallResultSavedAsFile(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "pages", Version: "test"}, nil)
	ro := true
	obj := map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}
	page := "line one\n" + strings.Repeat("頁", 20000)
	result := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: page}}}, nil
	}
	srv.AddTool(&mcp.Tool{Name: "Read-Page", InputSchema: obj, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: ro}}, result)
	srv.AddTool(&mcp.Tool{Name: "export_page", InputSchema: obj}, result) // not read-only: asks for approval
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(ts.Close)
	var conn store.Connector
	if code := e.do("POST", "/connectors", map[string]any{"name": "Pages", "type": "mcp_server",
		"config": map[string]any{"endpoint_url": ts.URL}}, &conn); code != 201 {
		t.Fatalf("create connector: %d", code)
	}
	researcher := team["researcher"]
	if code := e.do("PATCH", "/agents/"+researcher.ID.String(), map[string]any{"tool_connector_ids": []string{conn.ID.String()}}, nil); code != 200 {
		t.Fatalf("attach server: %d", code)
	}
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		if strings.Contains(brief(req), "A human approved the call to export_page") {
			if !strings.Contains(brief(req), "saved as the file results/pages__export_page-1.txt") {
				t.Errorf("the approved result should reach the agent as a saved file:\n%.2000s", brief(req))
			}
			return calls("complete_task", map[string]any{"summary": "ok"})
		}
		if len(toolResults(req)) == 0 {
			return calls("pages__Read-Page", map[string]any{"q": "a"})
		}
		if r := toolResults(req)[0]; !strings.Contains(r.Content, "saved as the file results/pages__read-page-1.txt") {
			t.Errorf("a direct MCP call should be saved under the name the agent used: %.200s", r.Content)
		}
		return calls("pages__export_page", map[string]any{"q": "a"})
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Export", "assignee_agent_id": researcher.ID}, &task)
	e.crew.RunPending(ctx, 1)

	var approvals []store.ToolApproval
	e.do("GET", "/tasks/"+task.ID.String()+"/approvals", nil, &approvals)
	if len(approvals) != 1 || approvals[0].CalledAs != "pages__export_page" {
		t.Fatalf("approvals: %+v", approvals)
	}
	if code := e.do("POST", "/tasks/"+task.ID.String()+"/approvals/"+approvals[0].ID.String(), map[string]any{"decision": "approve"}, nil); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	for i := 0; i < 100; i++ {
		if got, _ := e.store.GetTask(ctx, task.ID); got.Status == store.TaskTodo {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.do("GET", "/tasks/"+task.ID.String()+"/approvals", nil, &approvals)
	if approvals[0].ResultFile != "results/pages__export_page-1.txt" {
		t.Fatalf("the approval should record the saved file: %+v", approvals[0].ResultFile)
	}
	e.crew.RunPending(ctx, 1)
	files, _, _ := e.store.ListTaskFiles(ctx, task.ID, 10)
	if len(files) != 2 || files[0].CreatedBy != "researcher" || files[1].CreatedBy != "researcher" {
		t.Fatalf("files: %+v", files)
	}
	if got, _ := e.store.GetTask(ctx, task.ID); got.Status != store.TaskReview {
		t.Fatalf("the resumed run should have finished: %s", got.Status)
	}
}
