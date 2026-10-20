package crew

import (
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
// reads its ancestors' too, so a subtask can use what the task that delegated it saved.

const (
	// readFileDefault is how many characters read_file returns when no limit is given, and the most it
	// returns at once: with the header it stays under maxToolResult, so a read is never clipped.
	readFileDefault = 30000
	// briefFiles caps the files listed in the brief; the rest are counted and list_files shows them.
	briefFiles = 30
	// firstLineChars is how much of a file's first line the brief and list_files show.
	firstLineChars = 100
)

var fileTools = []tool{
	{name: "write_file", description: "Save text as a file of this task (creates it, or replaces a file of the same name). " +
		"Use it for notes, findings and drafts you or a later run will need; they stay out of the conversation until read. " +
		"Names may use folders, e.g. notes/plan.md.",
		schema: Obj(map[string]any{"name": Str("File name, e.g. notes.md or findings/hooks.md"),
			"content": Str("The whole text of the file")}, "name", "content"), run: toolWriteFile},
	{name: "read_file", description: fmt.Sprintf("Read a file of this task or of a task above it. Returns up to %d characters "+
		"from offset (a character position, default 0) and says how many characters the file has in all; call again with a "+
		"larger offset for the rest.", readFileDefault),
		schema: Obj(map[string]any{"name": Str("File name, as listed under Files"),
			"offset": Integer("First character to return (default 0)"),
			"limit":  Integer(fmt.Sprintf("Characters to return (default and maximum %d)", readFileDefault))}, "name"), run: toolReadFile},
	{name: "list_files", description: "List the files this task can read: its own and those of the tasks above it.",
		schema: Obj(map[string]any{}), run: toolListFiles},
}

func toolWriteFile(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct{ Name, Content string }
	if err := DecodeArgs(raw, &a); err != nil || strings.TrimSpace(a.Name) == "" {
		return "", errors.New("name is required")
	}
	if !utf8.ValidString(a.Content) {
		return "", errors.New("content must be text (valid UTF-8)")
	}
	f, err := e.c.store.WriteTaskFile(ctx, e.task.ID, a.Name, "", []byte(a.Content), e.agent.Slug)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Saved %s (%d characters).", f.Name, utf8.RuneCountInString(a.Content)), nil
}

func toolReadFile(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Name          string
		Offset, Limit int
	}
	if err := DecodeArgs(raw, &a); err != nil || strings.TrimSpace(a.Name) == "" {
		return "", errors.New("name is required")
	}
	if a.Offset < 0 {
		return "", errors.New("offset can't be negative")
	}
	if a.Limit <= 0 || a.Limit > readFileDefault {
		a.Limit = readFileDefault
	}
	f, rc, err := e.c.store.OpenTaskFile(ctx, e.task.ID, a.Name)
	if errors.Is(err, domain.ErrNotFound) {
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
	text := []rune(string(data))
	total := len(text)
	if a.Offset > 0 && a.Offset >= total {
		return "", fmt.Errorf("offset %d is past the end of %s, which has %d characters", a.Offset, f.Name, total)
	}
	end := min(a.Offset+a.Limit, total)
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s: characters %d–%d of %d.", f.Name, ownerNote(ctx, e, f), a.Offset, end, total)
	if end < total {
		fmt.Fprintf(&b, " Call read_file with offset %d for the rest.", end)
	}
	b.WriteString("\n\n")
	b.WriteString(string(text[a.Offset:end]))
	return b.String(), nil
}

func toolListFiles(ctx context.Context, e *runEnv, _ json.RawMessage) (string, error) {
	files, err := e.c.store.ListTaskFiles(ctx, e.task.ID)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "No files yet. Save one with write_file.", nil
	}
	var b strings.Builder
	owners := ownerNames(ctx, e.c.store, e.task.ID)
	for _, f := range files {
		b.WriteString(fileLine(ctx, e.c.store, e.task.ID, f, owners) + "\n")
	}
	return b.String(), nil
}

// filesBrief is the brief's Files section: what the run can read, without the contents.
func (c *Crew) filesBrief(ctx context.Context, task *store.Task) string {
	files, err := c.store.ListTaskFiles(ctx, task.ID)
	if err != nil || len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Files\nRead them with read_file; save your own with write_file.\n")
	owners := ownerNames(ctx, c.store, task.ID)
	for i, f := range files {
		if i == briefFiles {
			fmt.Fprintf(&b, "(%d more; call list_files)\n", len(files)-briefFiles)
			break
		}
		b.WriteString(fileLine(ctx, c.store, task.ID, f, owners) + "\n")
	}
	return b.String()
}

// fileLine renders one file as "- name (size, owner): first line".
func fileLine(ctx context.Context, st *store.Store, taskID uuid.UUID, f *store.TaskFile, owners map[uuid.UUID]string) string {
	line := fmt.Sprintf("- %s (%s", f.Name, humanBytes(f.SizeBytes))
	if f.TaskID != taskID {
		line += ", from " + owners[f.TaskID]
	}
	line += ")"
	if first := firstLine(ctx, st, taskID, f.Name); first != "" {
		line += ": " + first
	}
	return line
}

// firstLine is the start of a file's first non-empty line, read from its first few hundred bytes.
func firstLine(ctx context.Context, st *store.Store, taskID uuid.UUID, name string) string {
	_, rc, err := st.OpenTaskFile(ctx, taskID, name)
	if err != nil {
		return ""
	}
	defer rc.Close()
	head := make([]byte, 4*firstLineChars)
	n, _ := io.ReadFull(rc, head)
	text := strings.ToValidUTF8(string(head[:n]), "")
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return clip(l, firstLineChars)
		}
	}
	return ""
}

// ownerNames names the task's ancestors ("task #12") for files that aren't the task's own.
func ownerNames(ctx context.Context, st *store.Store, taskID uuid.UUID) map[uuid.UUID]string {
	out := map[uuid.UUID]string{}
	t, err := st.GetTask(ctx, taskID)
	for depth := 0; err == nil && t.ParentID != nil && depth < 20; depth++ {
		if t, err = st.GetTask(ctx, *t.ParentID); err == nil {
			out[t.ID] = fmt.Sprintf("task #%d", t.Number)
		}
	}
	return out
}

func ownerNote(ctx context.Context, e *runEnv, f *store.TaskFile) string {
	if f.TaskID == e.task.ID {
		return ""
	}
	return " (from " + ownerNames(ctx, e.c.store, e.task.ID)[f.TaskID] + ")"
}

// fileNames lists what a run can read, for a read_file of a name that doesn't exist.
func fileNames(ctx context.Context, e *runEnv) string {
	files, err := e.c.store.ListTaskFiles(ctx, e.task.ID)
	if err != nil || len(files) == 0 {
		return "this task has no files yet"
	}
	names := make([]string, 0, min(len(files), briefFiles))
	for i, f := range files {
		if i == briefFiles {
			names = append(names, "…")
			break
		}
		names = append(names, f.Name)
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
