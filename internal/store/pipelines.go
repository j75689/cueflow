package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/j75689/cueflow/internal/domain"
)

type Pipeline struct {
	ID                   uuid.UUID   `json:"id"`
	Name                 string      `json:"name"`
	Description          string      `json:"description"`
	IsActive             bool        `json:"is_active"`
	CronExpr             string      `json:"cron_expr"`
	Timezone             string      `json:"timezone"`
	Topic                string      `json:"topic"`
	Instructions         string      `json:"instructions"`
	Language             string      `json:"language"`
	Platforms            []string    `json:"platforms"`
	LLMConnectorID       *uuid.UUID  `json:"llm_connector_id"`
	SourceConnectorIDs   []uuid.UUID `json:"source_connector_ids"`
	SearchConnectorID    *uuid.UUID  `json:"search_connector_id"`
	SearchQueries        []string    `json:"search_queries"`
	PublisherConnectorID *uuid.UUID  `json:"publisher_connector_id"`
	NotifierConnectorID  *uuid.UUID  `json:"notifier_connector_id"`
	LookbackHours        int         `json:"lookback_hours"`
	MaxItems             int         `json:"max_items"`
	LastRunAt            *time.Time  `json:"last_run_at"`
	NextRunAt            *time.Time  `json:"next_run_at"` // filled in by the scheduler
	CreatedAt            time.Time   `json:"created_at"`
	UpdatedAt            time.Time   `json:"updated_at"`
}

const pipelineColumns = `id, name, description, is_active, cron_expr, timezone, topic, instructions, language,
	platforms, llm_connector_id, source_connector_ids, search_connector_id, search_queries, publisher_connector_id,
	notifier_connector_id, lookback_hours, max_items, last_run_at, created_at, updated_at`

func scanPipeline(row pgx.Row) (*Pipeline, error) {
	var p Pipeline
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &p.IsActive, &p.CronExpr, &p.Timezone, &p.Topic,
		&p.Instructions, &p.Language, &p.Platforms, &p.LLMConnectorID, &p.SourceConnectorIDs, &p.SearchConnectorID,
		&p.SearchQueries, &p.PublisherConnectorID, &p.NotifierConnectorID, &p.LookbackHours, &p.MaxItems,
		&p.LastRunAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if p.Platforms == nil {
		p.Platforms = []string{}
	}
	if p.SourceConnectorIDs == nil {
		p.SourceConnectorIDs = []uuid.UUID{}
	}
	if p.SearchQueries == nil {
		p.SearchQueries = []string{}
	}
	return &p, nil
}

// CronValidator is injected by the scheduler so the store does not depend on the cron library.
type CronValidator func(expr, tz string) error

func (p *Pipeline) normalize(validate CronValidator) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return invalid("name is required")
	}
	if p.Timezone == "" {
		p.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return invalid(fmt.Sprintf("unknown timezone %q", p.Timezone))
	}
	p.CronExpr = strings.TrimSpace(p.CronExpr)
	if validate != nil {
		if err := validate(p.CronExpr, p.Timezone); err != nil {
			return invalid(fmt.Sprintf("invalid cron expression: %v", err))
		}
	}
	if err := domain.ValidatePlatforms(p.Platforms); err != nil {
		return invalid(err.Error())
	}
	if p.Language == "" {
		p.Language = "zh-TW"
	}
	if p.LookbackHours <= 0 {
		p.LookbackHours = 24
	}
	if p.MaxItems <= 0 {
		p.MaxItems = 20
	}
	if p.MaxItems > 100 {
		p.MaxItems = 100
	}
	p.SearchQueries = nonNil(p.SearchQueries)
	if p.SourceConnectorIDs == nil {
		p.SourceConnectorIDs = []uuid.UUID{}
	}
	return nil
}

func (s *Store) ListPipelines(ctx context.Context) ([]*Pipeline, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+pipelineColumns+" FROM pipelines ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Pipeline{}
	for rows.Next() {
		p, err := scanPipeline(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetPipeline(ctx context.Context, id uuid.UUID) (*Pipeline, error) {
	p, err := scanPipeline(s.pool.QueryRow(ctx, "SELECT "+pipelineColumns+" FROM pipelines WHERE id=$1", id))
	return p, notFound(err)
}

func (s *Store) CreatePipeline(ctx context.Context, p *Pipeline, validate CronValidator) (*Pipeline, error) {
	if err := p.normalize(validate); err != nil {
		return nil, err
	}
	return scanPipeline(s.pool.QueryRow(ctx, `INSERT INTO pipelines (name, description, is_active, cron_expr, timezone,
		topic, instructions, language, platforms, llm_connector_id, source_connector_ids, search_connector_id,
		search_queries, publisher_connector_id, notifier_connector_id, lookback_hours, max_items)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) RETURNING `+pipelineColumns,
		p.Name, p.Description, p.IsActive, p.CronExpr, p.Timezone, p.Topic, p.Instructions, p.Language, p.Platforms,
		p.LLMConnectorID, p.SourceConnectorIDs, p.SearchConnectorID, p.SearchQueries, p.PublisherConnectorID,
		p.NotifierConnectorID, p.LookbackHours, p.MaxItems))
}

// UpdatePipeline replaces all editable fields (the API merges the patch onto the current row first).
func (s *Store) UpdatePipeline(ctx context.Context, p *Pipeline, validate CronValidator) (*Pipeline, error) {
	if err := p.normalize(validate); err != nil {
		return nil, err
	}
	out, err := scanPipeline(s.pool.QueryRow(ctx, `UPDATE pipelines SET name=$2, description=$3, is_active=$4,
		cron_expr=$5, timezone=$6, topic=$7, instructions=$8, language=$9, platforms=$10, llm_connector_id=$11,
		source_connector_ids=$12, search_connector_id=$13, search_queries=$14, publisher_connector_id=$15,
		notifier_connector_id=$16, lookback_hours=$17, max_items=$18, updated_at=NOW()
		WHERE id=$1 RETURNING `+pipelineColumns,
		p.ID, p.Name, p.Description, p.IsActive, p.CronExpr, p.Timezone, p.Topic, p.Instructions, p.Language,
		p.Platforms, p.LLMConnectorID, p.SourceConnectorIDs, p.SearchConnectorID, p.SearchQueries,
		p.PublisherConnectorID, p.NotifierConnectorID, p.LookbackHours, p.MaxItems))
	return out, notFound(err)
}

func (s *Store) DeletePipeline(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM pipelines WHERE id=$1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// --- runs ---

type RunLogEntry struct {
	At    time.Time `json:"at"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
}

type Run struct {
	ID           uuid.UUID      `json:"id"`
	PipelineID   uuid.UUID      `json:"pipeline_id"`
	PipelineName string         `json:"pipeline_name"`
	Trigger      string         `json:"trigger"`
	Status       string         `json:"status"`
	StartedAt    time.Time      `json:"started_at"`
	FinishedAt   *time.Time     `json:"finished_at"`
	Error        string         `json:"error"`
	Stats        map[string]any `json:"stats"`
	Log          []RunLogEntry  `json:"log"`
}

const runColumns = `r.id, r.pipeline_id, COALESCE(p.name,''), r.trigger, r.status, r.started_at, r.finished_at,
	r.error, r.stats, r.log`

func scanRun(row pgx.Row) (*Run, error) {
	var r Run
	var stats, log []byte
	if err := row.Scan(&r.ID, &r.PipelineID, &r.PipelineName, &r.Trigger, &r.Status, &r.StartedAt, &r.FinishedAt,
		&r.Error, &stats, &log); err != nil {
		return nil, err
	}
	r.Stats = map[string]any{}
	_ = json.Unmarshal(stats, &r.Stats)
	r.Log = []RunLogEntry{}
	_ = json.Unmarshal(log, &r.Log)
	return &r, nil
}

// StartRun records a RUNNING run, unless one is already running for the pipeline (returns ok=false).
func (s *Store) StartRun(ctx context.Context, pipelineID uuid.UUID, trigger string) (*Run, bool, error) {
	var out *Run
	ok := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialize per pipeline so cron and "run now" cannot overlap.
		if _, err := tx.Exec(ctx, "SELECT 1 FROM pipelines WHERE id=$1 FOR UPDATE", pipelineID); err != nil {
			return err
		}
		var running bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_runs WHERE pipeline_id=$1 AND status='RUNNING'
			AND started_at > NOW() - INTERVAL '2 hours')`, pipelineID).Scan(&running); err != nil {
			return err
		}
		if running {
			return nil
		}
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO pipeline_runs (pipeline_id, trigger) VALUES ($1,$2) RETURNING id`,
			pipelineID, trigger).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE pipelines SET last_run_at=NOW() WHERE id=$1", pipelineID); err != nil {
			return err
		}
		r, err := scanRun(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM pipeline_runs r LEFT JOIN pipelines p ON p.id=r.pipeline_id WHERE r.id=$1", id))
		out, ok = r, true
		return err
	})
	return out, ok, err
}

func (s *Store) AppendRunLog(ctx context.Context, runID uuid.UUID, e RunLogEntry) error {
	raw, _ := json.Marshal([]RunLogEntry{e})
	_, err := s.pool.Exec(ctx, "UPDATE pipeline_runs SET log = log || $2::jsonb WHERE id=$1", runID, raw)
	return err
}

func (s *Store) FinishRun(ctx context.Context, runID uuid.UUID, status, errMsg string, stats map[string]any) error {
	raw, _ := json.Marshal(stats)
	_, err := s.pool.Exec(ctx, `UPDATE pipeline_runs SET status=$2, error=$3, stats=$4, finished_at=NOW() WHERE id=$1`,
		runID, status, errMsg, raw)
	return err
}

// FailStaleRuns marks runs left RUNNING by a previous process as FAILED (called at startup).
func (s *Store) FailStaleRuns(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE pipeline_runs SET status='FAILED', error='interrupted (server restarted)',
		finished_at=NOW() WHERE status='RUNNING'`)
	return err
}

func (s *Store) GetRun(ctx context.Context, id uuid.UUID) (*Run, error) {
	r, err := scanRun(s.pool.QueryRow(ctx, "SELECT "+runColumns+" FROM pipeline_runs r LEFT JOIN pipelines p ON p.id=r.pipeline_id WHERE r.id=$1", id))
	return r, notFound(err)
}

func (s *Store) ListRuns(ctx context.Context, pipelineID *uuid.UUID, limit int) ([]*Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := "SELECT " + runColumns + " FROM pipeline_runs r LEFT JOIN pipelines p ON p.id=r.pipeline_id"
	args := []any{limit}
	if pipelineID != nil {
		q += " WHERE r.pipeline_id=$2"
		args = append(args, *pipelineID)
	}
	rows, err := s.pool.Query(ctx, q+" ORDER BY r.started_at DESC LIMIT $1", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Counts for the dashboard.
func (s *Store) Counts(ctx context.Context) (connTotal, connActive, pipeTotal, pipeActive int, err error) {
	err = s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM connectors), (SELECT count(*) FROM connectors WHERE is_active),
		(SELECT count(*) FROM pipelines), (SELECT count(*) FROM pipelines WHERE is_active)`).
		Scan(&connTotal, &connActive, &pipeTotal, &pipeActive)
	return
}
