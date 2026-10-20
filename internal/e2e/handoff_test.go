package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/j75689/cueflow/internal/llm"
	"github.com/j75689/cueflow/internal/store"
)

// setPrompt sets the prompt size the script model reports, until the test ends.
func setPrompt(t *testing.T, f func(req llm.Request) int64) {
	scriptMu.Lock()
	scriptPrompt = f
	scriptMu.Unlock()
	t.Cleanup(func() {
		scriptMu.Lock()
		scriptPrompt = nil
		scriptMu.Unlock()
	})
}

// lastUser is the newest user message of a request: the brief, or the crew's hand-off request.
func lastUser(req llm.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == llm.RoleUser {
			return req.Messages[i].Content
		}
	}
	return ""
}

func asksForHandOff(req llm.Request) bool {
	return strings.Contains(lastUser(req), "write a hand-off note with write_file")
}

func taskMessages(t *testing.T, e *env, id string) string {
	t.Helper()
	var msgs []store.TaskMessage
	e.do("GET", "/tasks/"+id+"/messages", nil, &msgs)
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Kind + ":" + m.Content + " " + string(m.Data) + "\n")
	}
	return b.String()
}

// Past 80% of the window the run is asked (in a new message at the end) for a hand-off note, writes
// it, and ends waiting for the human with the note in the thread. Their reply starts a fresh run
// whose brief carries the note.
func TestHandOffAtContextThreshold(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	// The first call of a run is small; from the second the context is 90% full.
	setPrompt(t, func(req llm.Request) int64 {
		if len(req.Messages) == 1 {
			return 100
		}
		return 900
	})
	runs := 0
	// A long CJK note (60 KB): the brief quotes its start, cut at a character, with a note of the rest.
	note := "Done: read the hooks. Next: write the plan.\n" + strings.Repeat("字", 20000)
	quoted := 0
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		b := brief(req)
		if strings.Contains(b, "## Hand-off from the previous run") {
			quoted++
			if !strings.Contains(b, "Done: read the hooks. Next: write the plan.") || !strings.Contains(b, "truncated: showed the first 12000") {
				t.Errorf("the fresh run's brief should carry the note:\n%.600s", b)
			}
			return calls("complete_task", map[string]any{"summary": "planned"})
		}
		if strings.Contains(b, "Looks right, go on.") && len(req.Messages) == 1 && runs > 1 {
			// A later run, after the task moved on from the hand-off: no quote.
			return calls("complete_task", map[string]any{"summary": "again"})
		}
		switch {
		case len(req.Messages) == 1:
			runs++
			return calls("add_comment", map[string]any{"content": "reading"})
		case asksForHandOff(req):
			if !strings.Contains(lastUser(req), "90% full (900 of 1000 tokens)") {
				t.Errorf("request: %s", lastUser(req))
			}
			// Earlier turns are sent as they were: the request only adds a message at the end.
			if req.Messages[0].Content != b || req.Messages[len(req.Messages)-1].Role != llm.RoleUser {
				t.Errorf("the request should be a new last message")
			}
			return calls("write_file", map[string]any{"name": "handoff.md", "content": note})
		default:
			return calls("add_comment", map[string]any{"content": "still reading"})
		}
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Plan Disney+", "assignee_agent_id": team["writer"].ID}, &task)
	e.crew.RunPending(ctx, 10)

	got, _ := e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskWaitingHuman {
		t.Fatalf("a hand-off should wait for the human: %s", got.Status)
	}
	var rs []store.AgentRun
	e.do("GET", "/tasks/"+task.ID.String()+"/runs", nil, &rs)
	if len(rs) != 1 || rs[0].Status != "SUCCEEDED" || rs[0].Turns != 3 {
		t.Fatalf("runs: %+v", rs)
	}
	thread := taskMessages(t, e, task.ID.String())
	// The thread gets a pointer, not a copy: the next brief quotes the note once.
	if !strings.Contains(thread, "message:Hand-off note saved as handoff.md (58.6 KB). ") ||
		!strings.Contains(thread, `"handoff": true`) && !strings.Contains(thread, `"handoff":true`) ||
		!strings.Contains(thread, "so it handed off") {
		t.Fatalf("thread:\n%s", thread)
	}

	// The human replies: a fresh run starts from the note and finishes.
	if code := e.do("POST", "/tasks/"+task.ID.String()+"/messages", map[string]any{"content": "Looks right, go on."}, nil); code != 201 {
		t.Fatalf("reply: %d", code)
	}
	e.crew.RunPending(ctx, 10)
	got, _ = e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskReview || runs != 1 || quoted != 1 {
		t.Fatalf("after the reply: %s, %d fresh starts, quoted %d times", got.Status, runs, quoted)
	}
	// Another reply later: that run didn't hand off, so the old note isn't quoted again.
	runs = 2
	e.do("POST", "/tasks/"+task.ID.String()+"/messages", map[string]any{"content": "One more thing."}, nil)
	e.crew.RunPending(ctx, 10)
	if quoted != 1 {
		t.Fatalf("a later brief quoted the old note (%d times)", quoted)
	}
}

// An agent that ignores the request gets one more turn, then the run ends saying no note was
// written. The request comes even when the threshold is crossed on the last allowed turn.
func TestHandOffWithoutNote(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	if code := e.do("PATCH", "/agents/"+team["writer"].ID.String(), map[string]any{"max_turns": 2}, nil); code != 200 {
		t.Fatalf("max_turns: %d", code)
	}
	setPrompt(t, func(req llm.Request) int64 {
		if len(req.Messages) == 1 {
			return 100
		}
		return 850
	})
	asked := 0 // requests in the latest conversation: the crew asks once
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		n := 0
		for _, m := range req.Messages {
			if m.Role == llm.RoleUser && strings.Contains(m.Content, "write a hand-off note with write_file") {
				n++
			}
		}
		asked = max(asked, n)
		return calls("add_comment", map[string]any{"content": "ignoring that"})
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Stubborn", "assignee_agent_id": team["writer"].ID}, &task)
	e.crew.RunPending(ctx, 10)

	var rs []store.AgentRun
	e.do("GET", "/tasks/"+task.ID.String()+"/runs", nil, &rs)
	// Turn 2 (the last allowed) crosses 80%; turns 3 and 4 are the hand-off's.
	if len(rs) != 1 || rs[0].Turns != 4 || asked != 1 {
		t.Fatalf("runs: %+v, asked %d", rs, asked)
	}
	if thread := taskMessages(t, e, task.ID.String()); !strings.Contains(thread, "none was saved") {
		t.Fatalf("thread:\n%s", thread)
	}
	if files, _, _ := e.store.ListTaskFiles(ctx, task.ID, 10); len(files) != 0 {
		t.Fatalf("no note should exist: %+v", files)
	}
}

// When the first prompt is already past the point, a fresh run wouldn't be smaller: the run says so
// once and carries on instead of handing off.
func TestNoHandOffWhenTheFirstPromptIsAlreadyFull(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	setPrompt(t, func(llm.Request) int64 { return 950 })
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		if asksForHandOff(req) {
			t.Error("asked for a hand-off although the first prompt was already full")
		}
		if len(toolResults(req)) == 0 {
			return calls("add_comment", map[string]any{"content": "working"})
		}
		return calls("complete_task", map[string]any{"summary": "done"})
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Big brief", "assignee_agent_id": team["writer"].ID}, &task)
	e.crew.RunPending(ctx, 10)
	got, _ := e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskReview {
		t.Fatalf("the run should finish normally: %s", got.Status)
	}
	if thread := taskMessages(t, e, task.ID.String()); strings.Count(thread, "won't hand off") != 1 {
		t.Fatalf("thread:\n%s", thread)
	}
}

// Asked for the note, an agent that answers in text has written it: the text is saved as the note.
// A comment the human left during the run re-queues the task, so the fresh run starts at once.
func TestHandOffNoteInTextAndCommentRequeues(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	setPrompt(t, func(req llm.Request) int64 {
		if len(req.Messages) == 1 {
			return 100
		}
		return 900
	})
	var taskID string
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		b := brief(req)
		switch {
		case strings.Contains(b, "## Hand-off from the previous run"):
			if !strings.Contains(b, "Left: the summary.") || !strings.Contains(b, "Also check the PRD") {
				t.Errorf("the fresh run should see the note and the comment:\n%s", b)
			}
			return calls("complete_task", map[string]any{"summary": "done"})
		case asksForHandOff(req):
			// The human comments while the run is still going.
			if code := e.do("POST", "/tasks/"+taskID+"/messages", map[string]any{"content": "Also check the PRD"}, nil); code != 201 {
				t.Errorf("comment: %d", code)
			}
			return llm.Message{Content: "Left: the summary."}
		case len(req.Messages) == 1:
			return calls("add_comment", map[string]any{"content": "reading"})
		}
		return calls("add_comment", map[string]any{"content": "more"})
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Summarise", "assignee_agent_id": team["writer"].ID}, &task)
	taskID = task.ID.String()
	e.crew.RunPending(ctx, 10)
	e.crew.RunPending(ctx, 10)

	got, _ := e.store.GetTask(ctx, task.ID)
	var rs []store.AgentRun
	e.do("GET", "/tasks/"+taskID+"/runs", nil, &rs)
	if got.Status != store.TaskReview || len(rs) != 2 {
		t.Fatalf("the comment should have re-queued the task into a fresh run: %s after %d runs", got.Status, len(rs))
	}
	if files, _, _ := e.store.ListTaskFiles(ctx, task.ID, 10); len(files) != 1 || files[0].Name != "handoff.md" || string(files[0].Head) != "Left: the summary." {
		t.Fatalf("the text answer should be the note: %+v", files)
	}
}

// A first prompt just under the point doesn't make every fresh run hand off on its second turn: the
// run hands off only once it has grown by a tenth of the window.
func TestNoHandOffWithoutGrowth(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	setPrompt(t, func(req llm.Request) int64 {
		if len(req.Messages) == 1 {
			return 790
		}
		return 820
	})
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		if asksForHandOff(req) {
			t.Error("asked for a hand-off after the context grew by 3% of the window")
		}
		if len(toolResults(req)) < 2 {
			return calls("add_comment", map[string]any{"content": "working"})
		}
		return calls("complete_task", map[string]any{"summary": "done"})
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Near the edge", "assignee_agent_id": team["writer"].ID}, &task)
	e.crew.RunPending(ctx, 10)
	got, _ := e.store.GetTask(ctx, task.ID)
	if got.Status != store.TaskReview {
		t.Fatalf("the run should finish: %s", got.Status)
	}
	if thread := taskMessages(t, e, task.ID.String()); strings.Count(thread, "won't hand off") != 1 {
		t.Fatalf("thread:\n%s", thread)
	}
}

// A note the run wrote before it was asked doesn't count as the answer: the request needs a new one.
func TestHandOffNeedsANoteWrittenAfterTheRequest(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	setPrompt(t, func(req llm.Request) int64 {
		if len(req.Messages) == 1 {
			return 100
		}
		return 900
	})
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		if len(req.Messages) == 1 {
			return calls("write_file", map[string]any{"name": "handoff.md", "content": "an old note"})
		}
		return calls("add_comment", map[string]any{"content": "busy"})
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Stale note", "assignee_agent_id": team["writer"].ID}, &task)
	e.crew.RunPending(ctx, 10)
	thread := taskMessages(t, e, task.ID.String())
	if !strings.Contains(thread, "none was saved") || strings.Contains(thread, "Hand-off note saved") {
		t.Fatalf("an earlier note was taken as the answer:\n%s", thread)
	}
}

// runAll runs queued tasks until none is left (automatic hand-offs queue the next run).
func runAll(e *env) {
	for range 12 {
		e.crew.RunPending(context.Background(), 10)
	}
}

// With Auto hand-off on, a hand-off with a note goes straight to a fresh run that starts from it, up
// to three in a row; the fourth waits for the human. A human comment starts the count again.
func TestAutoHandOff(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	writer := team["writer"]
	if code := e.do("PATCH", "/agents/"+writer.ID.String(), map[string]any{"auto_handoff": true}, nil); code != 200 {
		t.Fatalf("auto_handoff: %d", code)
	}
	var a store.Agent
	if e.do("GET", "/agents/"+writer.ID.String(), nil, &a); !a.AutoHandOff {
		t.Fatal("auto_handoff not saved")
	}
	setPrompt(t, func(req llm.Request) int64 {
		if len(req.Messages) == 1 {
			return 100
		}
		return 900
	})
	fresh := 0 // runs that started from a note
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		switch {
		case asksForHandOff(req):
			return calls("write_file", map[string]any{"name": "handoff.md", "content": "progress so far"})
		case len(req.Messages) == 1:
			if strings.Contains(brief(req), "## Hand-off from the previous run") {
				fresh++
			}
			return calls("add_comment", map[string]any{"content": "working"})
		}
		return calls("add_comment", map[string]any{"content": "more"})
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "Long job", "assignee_agent_id": writer.ID}, &task)
	runAll(e)

	var runs []store.AgentRun
	e.do("GET", "/tasks/"+task.ID.String()+"/runs", nil, &runs)
	got, _ := e.store.GetTask(ctx, task.ID)
	thread := taskMessages(t, e, task.ID.String())
	if len(runs) != 4 || got.Status != store.TaskWaitingHuman || fresh != 3 {
		t.Fatalf("want 3 automatic hand-offs then a stop: %d runs, %s, %d fresh starts", len(runs), got.Status, fresh)
	}
	for _, r := range runs {
		if r.Outcome != "handoff" {
			t.Fatalf("run outcome: %q", r.Outcome)
		}
	}
	if !strings.Contains(thread, "automatic hand-off 3 of 3") || !strings.Contains(thread, "This is the 4th hand-off in a row, so it waits for you") {
		t.Fatalf("thread:\n%s", thread)
	}

	// The human replies: the count starts again, so three more go on their own before the next stop.
	e.do("POST", "/tasks/"+task.ID.String()+"/messages", map[string]any{"content": "Keep going."}, nil)
	runAll(e)
	e.do("GET", "/tasks/"+task.ID.String()+"/runs", nil, &runs)
	thread = taskMessages(t, e, task.ID.String())
	if len(runs) != 8 || strings.Count(thread, "automatic hand-off 1 of 3") != 2 || strings.Count(thread, "4th hand-off in a row") != 2 {
		t.Fatalf("after the reply: %d runs\n%s", len(runs), thread)
	}
}

// A hand-off without a note (the agent ignored the request) waits for the human even with Auto
// hand-off on.
func TestAutoHandOffNeedsANote(t *testing.T) {
	e, team := crewSetup(t)
	ctx := context.Background()
	e.do("PATCH", "/agents/"+team["writer"].ID.String(), map[string]any{"auto_handoff": true}, nil)
	setPrompt(t, func(req llm.Request) int64 {
		if len(req.Messages) == 1 {
			return 100
		}
		return 900
	})
	setScript(t, func(t *testing.T, req llm.Request) llm.Message {
		return calls("add_comment", map[string]any{"content": "ignoring"})
	})
	var task store.Task
	e.do("POST", "/tasks", map[string]any{"title": "No note", "assignee_agent_id": team["writer"].ID}, &task)
	runAll(e)
	var runs []store.AgentRun
	e.do("GET", "/tasks/"+task.ID.String()+"/runs", nil, &runs)
	got, _ := e.store.GetTask(ctx, task.ID)
	if len(runs) != 1 || got.Status != store.TaskWaitingHuman || strings.Contains(taskMessages(t, e, task.ID.String()), "automatic hand-off") {
		t.Fatalf("a hand-off without a note must wait: %d runs, %s", len(runs), got.Status)
	}
}
