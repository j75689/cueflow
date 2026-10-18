package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/internalauth"
	"github.com/j75689/cueflow/internal/media"
	"github.com/j75689/cueflow/internal/store"
)

type mediaResp struct {
	*store.Media
	URL string `json:"url"`
}

func (s *Server) mediaService() *media.Service { return &media.Service{Store: s.Store, Hub: s.Hub} }

// uploadMedia stores an image or MP4 sent as multipart field "file".
func (s *Server) uploadMedia(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, media.MaxBytes+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "send the file as multipart field \"file\" (images up to 10 MB, MP4 up to 100 MB)")
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "send the file as multipart field \"file\" (images up to 10 MB, MP4 up to 100 MB)")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, media.MaxBytes+1))
	if err != nil {
		s.fail(w, err)
		return
	}
	m, err := s.mediaService().Upload(r.Context(), data)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, mediaResp{m, media.URLFor(m)})
}

// generateMedia makes an image with an image connector and stores it.
func (s *Server) generateMedia(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt      string     `json:"prompt"`
		Aspect      string     `json:"aspect"`
		ConnectorID *uuid.UUID `json:"connector_id"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	m, err := s.mediaService().Generate(ctx, media.GenerateRequest{Prompt: req.Prompt, Aspect: req.Aspect,
		ConnectorID: req.ConnectorID, CreatedBy: "human"})
	if err != nil {
		var ve *store.ValidationError
		if errors.As(err, &ve) || errors.Is(err, domain.ErrNotFound) {
			s.fail(w, err)
			return
		}
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, mediaResp{m, media.URLFor(m)})
}

// serveMedia serves a stored image. It is public (publishers fetch it without the
// admin token); the random UUID is the capability.
func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request) {
	id, ok := media.IDFromURL(media.PathPrefix + chi.URLParam(r, "file"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Backends that can sign URLs (GCS) serve the bytes themselves.
	if u, ok, err := s.Store.MediaRedirect(r.Context(), id, 15*time.Minute); err != nil {
		s.Logger.Warn("sign media url; serving through the app", "id", id, "err", err)
	} else if ok {
		w.Header().Set("Cache-Control", "private, max-age=600")
		http.Redirect(w, r, u, http.StatusFound)
		return
	}
	m, rc, err := s.Store.OpenMedia(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", m.MimeType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// ServeContent answers Range requests, which video players use to seek.
	http.ServeContent(w, r, "", time.Time{}, rc)
}

// registerMedia adopts a file a media producer (e.g. a video renderer) left in the bucket's staging area (see
// blob.StagingPrefix): POST /internal/media/register {"object":"uploads/<file>"}. The caller proves
// who it is with a Google ID token (MediaRegisterAuth); the video itself never passes through here.
func (s *Server) registerMedia(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Object string `json:"object"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	by, _, _ := strings.Cut(internalauth.EmailFrom(r.Context()), "@")
	m, err := s.Store.AdoptMedia(r.Context(), req.Object, by, media.SniffHead)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, mediaResp{m, media.URLFor(m)})
}

// deleteMedia removes a media item and its file; 409 while an unfinished post uses it.
func (s *Server) deleteMedia(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(strings.TrimSuffix(chi.URLParam(r, "id"), ".mp4"))
	if err != nil {
		s.fail(w, &store.ValidationError{Msg: "bad media id"})
		return
	}
	if err := s.Store.DeleteMedia(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
