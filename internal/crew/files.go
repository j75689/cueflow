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
	{name: "read_file", description: fmt.Sprintf("Read a file of this task or of a task above it. Returns up to %d characters "+
		"from offset (a character position, default 0) and says how many characters the file has in all; call again with a "+
		"larger offset for the rest.", readFileDefault),
		schema: Obj(map[string]any{"name": Str("File name, as listed under Files"),
			"offset": Integer("First character to return (default 0)"),
			"limit":  Integer(fmt.Sprintf("Characters to return (default and maximum %d)", readFileDefault))}, "name"), run: toolReadFile},
	{name: "list_files", description: fmt.Sprintf("List the files this task can read: its own and those of the tasks above it (at most %d, by name).", listFilesMax),
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
	msg := fmt.Sprintf("Saved %s (%d characters).", f.Name, utf8.RuneCountInString(a.Content))
	if n, ok, _ := e.c.store.AncestorTaskFile(ctx, e.task.ID, f.Name); ok {
		msg += fmt.Sprintf(" Task #%d has a file of the same name; read_file now returns yours, and theirs is unchanged.", n)
	}
	return msg, nil
}

func toolReadFile(ctx context.Context, e *runEnv, raw json.RawMessage) (string, error) {
	var a struct {
		Name          string
		Offset, Limit int
	}
	if err := DecodeArgs(raw, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Name) == "" {
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
	total := utf8.RuneCount(data)
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
	b.Write(runeSlice(data, a.Offset, end))
	return b.String(), nil
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

// fileLines renders one line per file, "- name (size, from task #N): first line", then how many
// were left out.
func fileLines(taskID uuid.UUID, files []*store.ListedTaskFile, total int, more string) (string, error) {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "- %s (%s", f.Name, humanBytes(f.SizeBytes))
		if f.TaskID != taskID {
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
