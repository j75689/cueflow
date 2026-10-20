package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/store"
)

// Task files over the API. A task's page reads its own files and its ancestors' (what the task's
// agent can read); writes and deletes touch its own only. Files are addressed by task and name,
// never by file id alone, and never through the public /media route.

const (
	// fileListMax caps one file listing.
	fileListMax = 500
	// filePageBytes is how much of a file one content request returns.
	filePageBytes = 64 << 10
)

// taskFileJSON is a file as the API shows it.
type taskFileJSON struct {
	store.TaskFile
	TaskNumber int64 `json:"task_number"`
	// Own is false for a file of an ancestor task, which this task can read but not change.
	Own bool `json:"own"`
	// Description is the start of the file's first line (for a saved tool result, the tool and its
	// arguments).
	Description string `json:"description"`
}

func (s *Server) listTaskFiles(w http.ResponseWriter, r *http.Request) {
	task, ok := s.fileTask(w, r)
	if !ok {
		return
	}
	files, total, err := s.Store.ListTaskFiles(r.Context(), task.ID, fileListMax)
	if err != nil {
		s.Fail(w, err)
		return
	}
	out := make([]taskFileJSON, 0, len(files))
	for _, f := range files {
		out = append(out, taskFileJSON{TaskFile: f.TaskFile, TaskNumber: f.TaskNumber, Own: f.TaskID == task.ID,
			Description: firstLine(f.Head, 200)})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"files": out, "total": total})
}

// taskFileContent returns one page of a file's text: up to 64 KB from byte offset, cut at character
// boundaries, and where the next page starts.
func (s *Server) taskFileContent(w http.ResponseWriter, r *http.Request) {
	task, ok := s.fileTask(w, r)
	if !ok {
		return
	}
	name, ok := fileName(w, r)
	if !ok {
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		offset = 0
	}
	// A few bytes more than a page, so a character cut at the end can be completed.
	f, data, err := s.Store.ReadTaskFileRange(r.Context(), task.ID, name, offset, filePageBytes+utf8.UTFMax)
	if err != nil {
		s.Fail(w, err)
		return
	}
	// Start on a character: skip continuation bytes an offset may have landed in.
	for len(data) > 0 && offset > 0 && !utf8.RuneStart(data[0]) {
		data, offset = data[1:], offset+1
	}
	end := min(len(data), filePageBytes)
	for end < len(data) && !utf8.RuneStart(data[end]) {
		end++ // finish the character the page would cut
	}
	page := data[:end]
	var next *int64
	if n := offset + int64(len(page)); n < f.SizeBytes {
		next = &n
	}
	binary := !utf8.Valid(page)
	text := ""
	if !binary {
		text = string(page)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"name": f.Name, "task_id": f.TaskID, "size_bytes": f.SizeBytes,
		"mime_type": f.MimeType, "offset": offset, "next_offset": next, "text": text, "binary": binary})
}

// downloadTaskFile streams a file as an attachment, sized from size_bytes; it is never read into
// memory here.
func (s *Server) downloadTaskFile(w http.ResponseWriter, r *http.Request) {
	task, ok := s.fileTask(w, r)
	if !ok {
		return
	}
	name, ok := fileName(w, r)
	if !ok {
		return
	}
	f, rc, err := s.Store.OpenTaskFile(r.Context(), task.ID, name)
	if err != nil {
		s.Fail(w, err)
		return
	}
	defer rc.Close()
	base := f.Name[strings.LastIndex(f.Name, "/")+1:]
	h := w.Header()
	// Always a download: a file's content is the agent's, and must not run as a page of this site.
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(base))
	h.Set("Content-Length", strconv.FormatInt(f.SizeBytes, 10))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-store")
	if _, err := io.Copy(w, rc); err != nil {
		s.Logger.Warn("stream task file", "task", task.ID, "file", f.Name, "err", err)
	}
}

// deleteTaskFile removes one of the task's own files. An ancestor's file is refused: this task can
// read it but not change it.
func (s *Server) deleteTaskFile(w http.ResponseWriter, r *http.Request) {
	task, ok := s.fileTask(w, r)
	if !ok {
		return
	}
	name, ok := fileName(w, r)
	if !ok {
		return
	}
	err := s.Store.DeleteTaskFile(r.Context(), task.ID, name)
	if errors.Is(err, domain.ErrNotFound) {
		if n, inAncestor, _ := s.Store.AncestorTaskFile(r.Context(), task.ID, name); inAncestor {
			WriteError(w, http.StatusForbidden, "forbidden", fmt.Sprintf("%s belongs to task #%d; delete it there", name, n))
			return
		}
	}
	if err != nil {
		s.Fail(w, err)
		return
	}
	s.Broker.Publish(task.OrgID, "task.file", map[string]any{"task_id": task.ID, "name": name, "action": "deleted"})
	w.WriteHeader(http.StatusNoContent)
}

// fileTask loads the task in the path; every file route goes through it.
func (s *Server) fileTask(w http.ResponseWriter, r *http.Request) (*store.Task, bool) {
	id, err := PathID(r)
	if err != nil {
		s.Fail(w, err)
		return nil, false
	}
	task, err := s.Store.GetTask(r.Context(), id)
	if err != nil {
		s.Fail(w, err)
		return nil, false
	}
	return task, true
}

// fileName reads ?name=, which may contain "/".
func fileName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.URL.Query().Get("name")
	if err := store.ValidTaskFileName(name); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid", err.Error())
		return "", false
	}
	return name, true
}

// firstLine is the start of the first non-empty line of a file's head.
func firstLine(head []byte, n int) string {
	text := strings.ToValidUTF8(string(head), "")
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if r := []rune(l); len(r) > n {
				return string(r[:n]) + "…"
			}
			return l
		}
	}
	return ""
}
