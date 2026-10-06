// Package media stores images CueFlow holds itself (reviewer uploads and AI-generated images) and
// generates images through image connectors. Posts reference stored images as "/media/{id}";
// the dispatch worker turns that into an absolute URL (CUEFLOW_MEDIA_BASE_URL) and also hands
// publishers the bytes, for services that cannot reach CueFlow (e.g. CueSocial on Cloud Run).
package media

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/j75689/cueflow/internal/connector"
	"github.com/j75689/cueflow/internal/domain"
	"github.com/j75689/cueflow/internal/hub"
	"github.com/j75689/cueflow/internal/store"
)

// PathPrefix is where stored images are served.
const PathPrefix = "/media/"

// Size limits: images (uploaded or generated) and videos (e.g. rendered Reels).
const (
	MaxImageBytes = 10 << 20
	MaxVideoBytes = 100 << 20
	// MaxBytes is the largest upload accepted at all.
	MaxBytes = MaxVideoBytes
)

var (
	imageTypes = []string{"image/png", "image/jpeg", "image/webp", "image/gif"}
	videoTypes = []string{"video/mp4"}
)

// IsVideo reports whether a MIME type is a supported video.
func IsVideo(mimeType string) bool { return slices.Contains(videoTypes, mimeType) }

// URL is the reference a post stores for an image.
func URL(id uuid.UUID) string { return PathPrefix + id.String() }

// URLFor is the reference for stored media; videos carry their extension so clients can tell.
func URLFor(m *store.Media) string {
	if IsVideo(m.MimeType) {
		return PathPrefix + m.ID.String() + ".mp4"
	}
	return URL(m.ID)
}

// IDFromURL recognizes CueFlow media URLs, relative ("/media/{id}") or absolute.
func IDFromURL(raw string) (uuid.UUID, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.HasPrefix(u.Path, PathPrefix) {
		return uuid.Nil, false
	}
	name := strings.TrimPrefix(u.Path, PathPrefix)
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}
	id, err := uuid.Parse(name)
	return id, err == nil
}

// Absolute resolves a relative media URL against base; other URLs are returned unchanged.
func Absolute(raw, base string) string {
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return strings.TrimRight(base, "/") + raw
	}
	return raw
}

// Sniff returns the type of data: a supported image or MP4 video, within its size limit.
func Sniff(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("file is empty")
	}
	mt := http.DetectContentType(data)
	switch {
	case slices.Contains(imageTypes, mt):
		if len(data) > MaxImageBytes {
			return "", fmt.Errorf("image is larger than %d MB", MaxImageBytes>>20)
		}
	case IsVideo(mt):
		if len(data) > MaxVideoBytes {
			return "", fmt.Errorf("video is larger than %d MB", MaxVideoBytes>>20)
		}
	default:
		return "", fmt.Errorf("unsupported file type %s (use PNG, JPEG, WebP, GIF or MP4)", mt)
	}
	return mt, nil
}

type Service struct {
	Store *store.Store
	Hub   *hub.Hub
}

// Upload stores an image or video a person (or a render service) uploaded.
func (s *Service) Upload(ctx context.Context, data []byte) (*store.Media, error) {
	mt, err := Sniff(data)
	if err != nil {
		return nil, &store.ValidationError{Msg: err.Error()}
	}
	return s.Store.CreateMedia(ctx, store.NewMedia{MimeType: mt, Data: data, Source: "upload"})
}

type GenerateRequest struct {
	Prompt string
	Aspect string
	// ConnectorID picks the image connector; nil uses the first active one.
	ConnectorID *uuid.UUID
	// CreatedBy is "human" or the agent slug.
	CreatedBy string
}

// Generate makes one image and stores it.
func (s *Service) Generate(ctx context.Context, req GenerateRequest) (*store.Media, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, &store.ValidationError{Msg: "an image prompt is required"}
	}
	aspect := req.Aspect
	if aspect == "" {
		aspect = "1:1"
	}
	if !slices.Contains(connector.Aspects, aspect) {
		return nil, &store.ValidationError{Msg: fmt.Sprintf("aspect must be one of %s", strings.Join(connector.Aspects, ", "))}
	}
	id := req.ConnectorID
	if id == nil {
		c, err := s.Store.FirstActiveConnector(ctx, connector.KindImage)
		if errors.Is(err, domain.ErrNotFound) {
			return nil, &store.ValidationError{Msg: "no image generator configured: add one under Connectors"}
		}
		if err != nil {
			return nil, err
		}
		id = &c.ID
	}
	gen, _, err := s.Hub.ImageGenerator(ctx, *id)
	if err != nil {
		return nil, err
	}
	defer hub.Close(gen)
	images, err := gen.GenerateImage(ctx, connector.ImageRequest{Prompt: prompt, Aspect: aspect, N: 1})
	if err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, errors.New("the image generator returned no image")
	}
	mt, err := Sniff(images[0].Data)
	if err == nil && IsVideo(mt) {
		err = errors.New("a video, not an image")
	}
	if err != nil {
		return nil, fmt.Errorf("the image generator returned %w", err)
	}
	return s.Store.CreateMedia(ctx, store.NewMedia{MimeType: mt, Data: images[0].Data, Source: "generated",
		Prompt: prompt, ConnectorID: id, CreatedBy: req.CreatedBy})
}
