package crew

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

// Task files are an agent's working storage for one task (store.TaskFile): notes, findings, drafts
// too long for the thread. Every agent has the tools. A run writes only its own task's files and
// reads its ancestors' too, so a subtask can use what the task that delegated it saved, and its
// direct subtasks' (by naming the subtask), so a task can use what it delegated.

const (
	// briefFiles caps the files listed in the brief; the rest are counted and list_files shows them.
	briefFiles = 30
	// listFilesMax caps one list_files answer; it says how many more there are.
	listFilesMax = 100
	// firstLineChars is how much of a file's first line the brief and list_files show.
	firstLineChars = 100
)

var fileTools = []tool{
	{name: "write_file", description: "Save text as a file of this task (creates it, or replaces a file of the same name). " +
		"Use it for notes, findings and drafts you or a later run will need; they stay out of the conversation until read. " +
		"Names may use folders, e.g. notes/plan.md.",
		schema: Obj(map[string]any{"name": Str("File name, e.g. notes.md or findings/hooks.md"),
			"content": Str("The whole text of the file")}, "name", "content"), run: toolWriteFile},
	{name: "read_file", description: fmt.Sprintf("Read a file of this task, of a task above it, or (with task) of one of its "+
		"direct subtasks. Returns up to %d characters from offset (a character position, default 0) and says how many "+
		"characters the file has in all; call again with a larger offset for the rest.", PageChars),
		schema: Obj(map[string]any{"name": Str("File name, as listed under Files"),
			"task":   Integer("Number of the task the file belongs to; needed for a file of a subtask (shown as \"from subtask #N\")."),
			"offset": Integer("First character to return (default 0)"),
			"limit":  Integer(fmt.Sprintf("Characters to return (default and maximum %d)", PageChars))}, "name"), run: toolReadFile},
	{name: "list_files", description: fmt.Sprintf("List the files this task can read: its own, those of the tasks above it and those "+
		"of its direct subtasks (at most %d, by name).", listFilesMax),
		schema: Obj(map[string]any{}), run: toolListFiles},
}

func toolWriteFile(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ Name, Content string }
	if err := DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Name) == "" {
		return "", errors.New("name is required")
	}
	if !utf8.ValidString(a.Content) {
		return "", errors.New("content must be text (valid UTF-8)")
	}
	f, err := e.c.store.WriteTaskFile(ctx, e.task.ID, a.Name, "", []byte(a.Content), e.agent.Slug)
	if err != nil {
		return "", err
	}
	e.c.fileChanged(ctx, e.task.ID, f.Name)
	if f.Name == handOffFile {
		e.wroteHandOff = true
	}
	msg := fmt.Sprintf("Saved %s (%d characters).", f.Name, utf8.RuneCountInString(a.Content))
	if n, ok, _ := e.c.store.AncestorTaskFile(ctx, e.task.ID, f.Name); ok {
		msg += fmt.Sprintf(" Task #%d has a file of the same name; read_file now returns yours, and theirs is unchanged.", n)
	}
	return msg, nil
}

func toolReadFile(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Name                string
		Task, Offset, Limit int
	}
	if err := DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Name) == "" {
		return "", errors.New("name is required")
	}
	owner := uuid.Nil
	if a.Task != 0 {
		t, err := e.c.store.GetTaskByNumber(ctx, e.task.OrgID, int64(a.Task))
		if errors.Is(err, domain.ErrNotFound) {
			return "", fmt.Errorf("there is no task #%d", a.Task)
		}
		if err != nil {
			return "", err
		}
		owner = t.ID
	}
	f, rc, err := e.c.store.OpenTaskFile(ctx, e.task.ID, owner, a.Name)
	if errors.Is(err, store.ErrTaskFileNotReadable) {
		return "", fmt.Errorf("task #%d is not this task, a task above it or one of its direct subtasks; nothing was read", a.Task)
	}
	if errors.Is(err, domain.ErrNotFound) {
		if owner != uuid.Nil {
			return "", fmt.Errorf("task #%d has no file named %q", a.Task, a.Name)
		}
		if subs, _ := e.c.store.SubtasksWithFile(ctx, e.task.ID, a.Name); len(subs) > 0 {
			nums := make([]string, len(subs))
			for i, n := range subs {
				nums[i] = fmt.Sprintf("#%d", n)
			}
			return "", fmt.Errorf("there is no file named %q in this task or above it; subtask %s has one: "+
				"call read_file with task set to that number", a.Name, strings.Join(nums, ", "))
		}
		return "", fmt.Errorf("there is no file named %q; %s", a.Name, fileNames(ctx, e))
	}
	if err != nil {
		return "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s is not text (%d bytes) and can't be shown", f.Name, len(data))
	}
	return PageText(f.Name+ownerNote(ctx, e, f)+": ", data, a.Offset, a.Limit, "read_file", f.Name)
}

// runeSlice returns characters [from, to) of valid UTF-8 data without decoding the rest.
func runeSlice(data []byte, from, to int) []byte {
	start, i := -1, 0
	for pos := 0; pos < len(data); i++ {
		if i == from {
			start = pos
		}
		if i == to {
			return data[start:pos]
		}
		_, size := utf8.DecodeRune(data[pos:])
		pos += size
	}
	if start < 0 {
		start = len(data)
	}
	return data[start:]
}

func toolListFiles(ctx context.Context, e *runEnv, _ json.RawMessage) (string, error) {
	files, total, err := e.c.store.ListTaskFiles(ctx, e.task.ID, listFilesMax)
	if err != nil {
		return "", err
	}
	if total == 0 {
		return "No files yet. Save one with write_file.", nil
	}
	return fileLines(e.task.ID, files, total, "")
}

// filesBrief is the brief's Files section: what the run can read, without the contents.
func (c *Crew) filesBrief(ctx context.Context, task *store.Task) string {
	files, total, err := c.store.ListTaskFiles(ctx, task.ID, briefFiles)
	if err != nil {
		c.logger.Warn("list task files for the brief", "task", task.ID, "err", err)
		return "\n## Files\n(The file list is unavailable right now; call list_files.)\n"
	}
	if total == 0 {
		return ""
	}
	lines, _ := fileLines(task.ID, files, total, "call list_files")
	return "\n## Files\nRead them with read_file; save your own with write_file.\n" + lines
}

// fileLines renders one line per file, "- name (size, from task #N): first line" (or "from subtask
// #N" for a direct subtask's), then how many were left out.
func fileLines(taskID uuid.UUID, files []*store.ListedTaskFile, total int, more string) (string, error) {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "- %s (%s", f.Name, humanBytes(f.SizeBytes))
		if f.Subtask {
			fmt.Fprintf(&b, ", from subtask #%d", f.TaskNumber)
		} else if f.TaskID != taskID {
			fmt.Fprintf(&b, ", from task #%d", f.TaskNumber)
		}
		b.WriteString(")")
		if first := firstLine(f.Head); first != "" {
			b.WriteString(": " + first)
		}
		b.WriteString("\n")
	}
	if rest := total - len(files); rest > 0 {
		if more != "" {
			fmt.Fprintf(&b, "(%d more; %s)\n", rest, more)
		} else {
			fmt.Fprintf(&b, "(%d more not listed)\n", rest)
		}
	}
	return b.String(), nil
}

// firstLine is the start of the first non-empty line of a file's head.
func firstLine(head []byte) string {
	text := strings.ToValidUTF8(string(head), "")
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return clip(l, firstLineChars)
		}
	}
	return ""
}

func ownerNote(ctx context.Context, e *runEnv, f *store.TaskFile) string {
	if f.TaskID == e.task.ID {
		return ""
	}
	if t, err := e.c.store.GetTask(ctx, f.TaskID); err == nil {
		if t.ParentID != nil && *t.ParentID == e.task.ID {
			return fmt.Sprintf(" (from subtask #%d)", t.Number)
		}
		return fmt.Sprintf(" (from task #%d)", t.Number)
	}
	return " (from a task above this one)"
}

// fileNames lists what a run can read, for a read_file of a name that doesn't exist.
func fileNames(ctx context.Context, e *runEnv) string {
	files, total, err := e.c.store.ListTaskFiles(ctx, e.task.ID, briefFiles)
	if err != nil || total == 0 {
		return "this task has no files yet"
	}
	names := make([]string, 0, len(files)+1)
	for _, f := range files {
		names = append(names, f.Name)
	}
	if total > len(files) {
		names = append(names, "…")
	}
	return "the files you can read are: " + strings.Join(names, ", ")
}

func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

const (
	// saveResultOver is the length above which a tool result is saved as a task file and the model
	// gets its start plus where to read the rest. Below it reading the result inline is cheaper than
	// a read_file round trip.
	saveResultOver = 8000
	// savedResultHead is how much of a saved result the model sees inline.
	savedResultHead = 2000
	// savedResultArgs caps the arguments quoted in a saved result's first line.
	savedResultArgs = 200
)

// saveLargeResult keeps a long result of a tool marked saveLarge out of the conversation: it saves it as a file of the
// run's own task, named results/<tool>-<n>.txt with n counting that tool's saved results in the task,
// and returns what the model gets instead (where the file is and its start). The file starts with a
// line naming the tool and its arguments, which is what the brief's Files list shows. ok is false
// when the result stays inline: short enough, not text, or the save failed (logged; the caller
// clips it as before).
func (c *Crew) saveLargeResult(ctx context.Context, taskID uuid.UUID, author, toolName string, args json.RawMessage, result string) (stub, name string, ok bool) {
	total := utf8.RuneCountInString(result)
	if total <= saveResultOver || !utf8.ValidString(result) {
		return "", "", false
	}
	header := fmt.Sprintf("# Result of %s %s\n\n", toolName, clip(compactJSON(args), savedResultArgs))
	if len(header)+len(result) > store.MaxTaskFileBytes {
		return "", "", false // too big to keep; clipped inline as before
	}
	data := []byte(header + result)
	base := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(toolName), "_"), "_")
	if base == "" {
		base = "tool"
	}
	prefix := "results/" + base + "-"
	n, err := c.store.CountTaskFiles(ctx, taskID, prefix)
	for attempt := 0; err == nil && attempt < 20; attempt++ {
		name = fmt.Sprintf("%s%d.txt", prefix, n+1+attempt)
		_, err = c.store.CreateTaskFile(ctx, taskID, name, "", data, author)
		if errors.Is(err, store.ErrTaskFileExists) {
			err = nil // taken (an earlier run, or one the agent wrote itself): try the next number
			continue
		}
		if err == nil {
			c.fileChanged(ctx, taskID, name)
			skip := utf8.RuneCountInString(header) + savedResultHead
			stub = fmt.Sprintf("%s returned %d characters, saved as the file %s. The first %d are below; "+
				"call read_file with name %q and offset %d for the rest.\n\n%s",
				toolName, total, name, savedResultHead, name, skip, string(runeSlice([]byte(result), 0, savedResultHead)))
			return stub, name, true
		}
	}
	c.logger.Warn("save a large tool result as a task file; keeping it inline", "task", taskID, "tool", toolName, "err", err)
	return "", "", false
}

// compactJSON renders tool arguments on one line.
func compactJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return strings.Join(strings.Fields(string(raw)), " ")
	}
	return b.String()
}

// fileChanged tells open pages of the task (in its org) that one of its files was written.
func (c *Crew) fileChanged(ctx context.Context, taskID uuid.UUID, name string) {
	c.broker.Publish(c.taskOrg(ctx, taskID), "task.file", map[string]any{"task_id": taskID, "name": name, "action": "written"})
}
