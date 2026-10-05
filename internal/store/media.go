package store

import (
	"context"

	"github.com/google/uuid"
)

// Media is an image CueFlow stores itself (uploaded or generated).
type Media struct {
	ID          uuid.UUID  `json:"id"`
	MimeType    string     `json:"mime_type"`
	SizeBytes   int        `json:"size_bytes"`
	Source      string     `json:"source"` // upload | generated
	Prompt      string     `json:"prompt"`
	ConnectorID *uuid.UUID `json:"connector_id"`
	CreatedBy   string     `json:"created_by"`
	Data        []byte     `json:"-"`
}

type NewMedia struct {
	MimeType    string
	Data        []byte
	Source      string
	Prompt      string
	ConnectorID *uuid.UUID
	CreatedBy   string
}

func (s *Store) CreateMedia(ctx context.Context, in NewMedia) (*Media, error) {
	if len(in.Data) == 0 {
		return nil, invalid("image is empty")
	}
	if in.CreatedBy == "" {
		in.CreatedBy = "human"
	}
	m := &Media{MimeType: in.MimeType, SizeBytes: len(in.Data), Source: in.Source, Prompt: in.Prompt,
		ConnectorID: in.ConnectorID, CreatedBy: in.CreatedBy}
	err := s.pool.QueryRow(ctx, `INSERT INTO media (mime_type, data, size_bytes, source, prompt, connector_id, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		in.MimeType, in.Data, len(in.Data), in.Source, in.Prompt, in.ConnectorID, in.CreatedBy).Scan(&m.ID)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// GetMedia returns an image with its bytes.
func (s *Store) GetMedia(ctx context.Context, id uuid.UUID) (*Media, error) {
	m := &Media{ID: id}
	err := s.pool.QueryRow(ctx, `SELECT mime_type, size_bytes, source, prompt, connector_id, created_by, data
		FROM media WHERE id=$1`, id).Scan(&m.MimeType, &m.SizeBytes, &m.Source, &m.Prompt, &m.ConnectorID, &m.CreatedBy, &m.Data)
	if err != nil {
		return nil, notFound(err)
	}
	return m, nil
}
