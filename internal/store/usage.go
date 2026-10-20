package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type UsageTotals struct {
	Runs           int64 `json:"runs"`
	FailedRuns     int64 `json:"failed_runs"`
	InputTokens    int64 `json:"input_tokens"`
	OutputTokens   int64 `json:"output_tokens"`
	ToolCalls      int64 `json:"tool_calls"`
	UnreportedRuns int64 `json:"unreported_runs"`
	// CacheReadTokens and CacheWriteTokens are the parts of InputTokens read from and written to the
	// providers' prompt caches.
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

type UsageDay struct {
	Date             string `json:"date"`
	Runs             int64  `json:"runs"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

type UsageSource struct {
	Source           string `json:"source"`
	Runs             int64  `json:"runs"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

type UsageAgent struct {
	AgentID          uuid.UUID `json:"agent_id"`
	Name             string    `json:"name"`
	Avatar           string    `json:"avatar"`
	Slug             string    `json:"slug"`
	Runs             int64     `json:"runs"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
}

type UsageModel struct {
	ConnectorID      *uuid.UUID `json:"connector_id"`
	ConnectorName    string     `json:"connector_name"`
	ConnectorType    string     `json:"connector_type"`
	Runtime          string     `json:"runtime"`
	Model            string     `json:"model"`
	Runs             int64      `json:"runs"`
	InputTokens      int64      `json:"input_tokens"`
	OutputTokens     int64      `json:"output_tokens"`
	CacheReadTokens  int64      `json:"cache_read_tokens"`
	CacheWriteTokens int64      `json:"cache_write_tokens"`
}

type UsageOrg struct {
	OrgID            uuid.UUID `json:"org_id"`
	Name             string    `json:"name"`
	Slug             string    `json:"slug"`
	Runs             int64     `json:"runs"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
}

type UsageTask struct {
	TaskID           uuid.UUID `json:"task_id"`
	Number           int64     `json:"number"`
	Title            string    `json:"title"`
	Runs             int64     `json:"runs"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
}

type Usage struct {
	From     time.Time     `json:"from"`
	To       time.Time     `json:"to"`
	TZ       string        `json:"tz"`
	Totals   UsageTotals   `json:"totals"`
	Daily    []UsageDay    `json:"daily"`
	BySource []UsageSource `json:"by_source"`
	ByOrg    []UsageOrg    `json:"by_org"`
	ByAgent  []UsageAgent  `json:"by_agent"`
	ByModel  []UsageModel  `json:"by_model"`
	TopTasks []UsageTask   `json:"top_tasks"`
}

// usageRuns is the agent runs started since $1, in the shape the report aggregates (source is
// "agent"; the column stays so the report can take other kinds of runs later).
const usageRuns = `WITH runs AS (
	SELECT r.started_at, 'agent' AS source, r.org_id, r.agent_id, r.task_id, r.connector_id, r.model, r.runtime,
	       r.input_tokens AS inp, r.output_tokens AS outp, r.tool_calls::bigint AS calls, r.status,
	       r.cache_read_tokens AS cread, r.cache_write_tokens AS cwrite
	FROM agent_runs r WHERE r.started_at >= $1
) `

// UsageReport aggregates token usage since `since`, bucketing days in tz.
func (s *Store) UsageReport(ctx context.Context, since time.Time, tz string) (*Usage, error) {
	u := &Usage{From: since, To: time.Now(), TZ: tz, Daily: []UsageDay{}, BySource: []UsageSource{}, ByOrg: []UsageOrg{},
		ByAgent: []UsageAgent{}, ByModel: []UsageModel{}, TopTasks: []UsageTask{}}

	if err := s.pool.QueryRow(ctx, usageRuns+`SELECT count(*), count(*) FILTER (WHERE status = 'FAILED'),
		COALESCE(sum(inp), 0)::bigint, COALESCE(sum(outp), 0)::bigint, COALESCE(sum(calls), 0)::bigint,
		count(*) FILTER (WHERE status = 'SUCCEEDED' AND inp = 0 AND outp = 0),
		COALESCE(sum(cread), 0)::bigint, COALESCE(sum(cwrite), 0)::bigint
		FROM runs`, since).Scan(&u.Totals.Runs, &u.Totals.FailedRuns, &u.Totals.InputTokens,
		&u.Totals.OutputTokens, &u.Totals.ToolCalls, &u.Totals.UnreportedRuns,
		&u.Totals.CacheReadTokens, &u.Totals.CacheWriteTokens); err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, usageRuns+`, days AS (
			SELECT generate_series(date_trunc('day', $1::timestamptz AT TIME ZONE $2),
			                       date_trunc('day', now() AT TIME ZONE $2), interval '1 day') AS day
		), agg AS (
			SELECT date_trunc('day', started_at AT TIME ZONE $2) AS day, count(*) AS runs,
			       sum(inp)::bigint AS inp, sum(outp)::bigint AS outp,
			       sum(cread)::bigint AS cread, sum(cwrite)::bigint AS cwrite
			FROM runs GROUP BY 1
		)
		SELECT to_char(d.day, 'YYYY-MM-DD'), COALESCE(a.runs, 0), COALESCE(a.inp, 0), COALESCE(a.outp, 0),
		       COALESCE(a.cread, 0), COALESCE(a.cwrite, 0)
		FROM days d LEFT JOIN agg a ON a.day = d.day ORDER BY d.day`, since, tz)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d UsageDay
		if err := rows.Scan(&d.Date, &d.Runs, &d.InputTokens, &d.OutputTokens, &d.CacheReadTokens, &d.CacheWriteTokens); err != nil {
			rows.Close()
			return nil, err
		}
		u.Daily = append(u.Daily, d)
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, usageRuns+`SELECT source, count(*), COALESCE(sum(inp), 0)::bigint, COALESCE(sum(outp), 0)::bigint, COALESCE(sum(cread), 0)::bigint, COALESCE(sum(cwrite), 0)::bigint
		FROM runs GROUP BY source ORDER BY sum(inp) + sum(outp) DESC`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v UsageSource
		if err := rows.Scan(&v.Source, &v.Runs, &v.InputTokens, &v.OutputTokens, &v.CacheReadTokens, &v.CacheWriteTokens); err != nil {
			rows.Close()
			return nil, err
		}
		u.BySource = append(u.BySource, v)
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, usageRuns+`SELECT o.id, o.name, o.slug, count(*),
		COALESCE(sum(inp), 0)::bigint, COALESCE(sum(outp), 0)::bigint, COALESCE(sum(cread), 0)::bigint, COALESCE(sum(cwrite), 0)::bigint
		FROM runs JOIN orgs o ON o.id = runs.org_id
		GROUP BY o.id, o.name, o.slug ORDER BY sum(inp) + sum(outp) DESC, count(*) DESC`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v UsageOrg
		if err := rows.Scan(&v.OrgID, &v.Name, &v.Slug, &v.Runs, &v.InputTokens, &v.OutputTokens, &v.CacheReadTokens, &v.CacheWriteTokens); err != nil {
			rows.Close()
			return nil, err
		}
		u.ByOrg = append(u.ByOrg, v)
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, usageRuns+`SELECT a.id, a.name, a.avatar, a.slug, count(*),
		COALESCE(sum(inp), 0)::bigint, COALESCE(sum(outp), 0)::bigint, COALESCE(sum(cread), 0)::bigint, COALESCE(sum(cwrite), 0)::bigint
		FROM runs JOIN agents a ON a.id = runs.agent_id
		GROUP BY a.id, a.name, a.avatar, a.slug ORDER BY sum(inp) + sum(outp) DESC, count(*) DESC`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v UsageAgent
		if err := rows.Scan(&v.AgentID, &v.Name, &v.Avatar, &v.Slug, &v.Runs, &v.InputTokens, &v.OutputTokens,
			&v.CacheReadTokens, &v.CacheWriteTokens); err != nil {
			rows.Close()
			return nil, err
		}
		u.ByAgent = append(u.ByAgent, v)
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, usageRuns+`SELECT runs.connector_id, COALESCE(c.name, ''), COALESCE(c.type, ''),
		runs.runtime, runs.model, count(*), COALESCE(sum(inp), 0)::bigint, COALESCE(sum(outp), 0)::bigint, COALESCE(sum(cread), 0)::bigint, COALESCE(sum(cwrite), 0)::bigint
		FROM runs LEFT JOIN connectors c ON c.id = runs.connector_id
		GROUP BY runs.connector_id, c.name, c.type, runs.runtime, runs.model
		ORDER BY sum(inp) + sum(outp) DESC, count(*) DESC`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v UsageModel
		if err := rows.Scan(&v.ConnectorID, &v.ConnectorName, &v.ConnectorType, &v.Runtime, &v.Model, &v.Runs,
			&v.InputTokens, &v.OutputTokens, &v.CacheReadTokens, &v.CacheWriteTokens); err != nil {
			rows.Close()
			return nil, err
		}
		u.ByModel = append(u.ByModel, v)
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, usageRuns+`SELECT t.id, t.number, t.title, count(*),
		COALESCE(sum(inp), 0)::bigint, COALESCE(sum(outp), 0)::bigint, COALESCE(sum(cread), 0)::bigint, COALESCE(sum(cwrite), 0)::bigint
		FROM runs JOIN tasks t ON t.id = runs.task_id
		GROUP BY t.id, t.number, t.title ORDER BY sum(inp) + sum(outp) DESC, count(*) DESC LIMIT 10`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v UsageTask
		if err := rows.Scan(&v.TaskID, &v.Number, &v.Title, &v.Runs, &v.InputTokens, &v.OutputTokens, &v.CacheReadTokens, &v.CacheWriteTokens); err != nil {
			return nil, err
		}
		u.TopTasks = append(u.TopTasks, v)
	}
	return u, rows.Err()
}
