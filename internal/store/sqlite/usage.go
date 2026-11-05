package sqlite

import (
	"context"
	"time"

	"github.com/j75689/cueflow/internal/store"
)

// usageRuns is the agent runs started since $1, in the shape the report aggregates (source is
// "agent"; the column stays so the report can take other kinds of runs later).
const usageRuns = `WITH runs AS (
	SELECT r.started_at, 'agent' AS source, r.org_id, r.agent_id, r.task_id, r.connector_id, r.model, r.runtime,
	       r.input_tokens AS inp, r.output_tokens AS outp, r.tool_calls AS calls, r.status,
	       r.cache_read_tokens AS cread, r.cache_write_tokens AS cwrite
	FROM agent_runs r WHERE r.started_at >= $1
) `

// UsageReport aggregates token usage since `since`, bucketing days in tz.
func (s *Store) UsageReport(ctx context.Context, since time.Time, tz string) (*store.Usage, error) {
	u := &store.Usage{From: since, To: time.Now(), TZ: tz, Daily: []store.UsageDay{}, BySource: []store.UsageSource{}, ByOrg: []store.UsageOrg{},
		ByAgent: []store.UsageAgent{}, ByModel: []store.UsageModel{}, TopTasks: []store.UsageTask{}}

	if err := s.r.QueryRow(ctx, usageRuns+`SELECT count(*), count(*) FILTER (WHERE status = 'FAILED'),
		COALESCE(sum(inp), 0), COALESCE(sum(outp), 0), COALESCE(sum(calls), 0),
		count(*) FILTER (WHERE status = 'SUCCEEDED' AND inp = 0 AND outp = 0),
		COALESCE(sum(cread), 0), COALESCE(sum(cwrite), 0)
		FROM runs`, since).Scan(&u.Totals.Runs, &u.Totals.FailedRuns, &u.Totals.InputTokens,
		&u.Totals.OutputTokens, &u.Totals.ToolCalls, &u.Totals.UnreportedRuns,
		&u.Totals.CacheReadTokens, &u.Totals.CacheWriteTokens); err != nil {
		return nil, err
	}

	// Days are bucketed here, in tz: the runs are read in UTC.
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, err
	}
	day := func(t time.Time) time.Time {
		y, m, d := t.In(loc).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	index := map[time.Time]int{} // day -> its place in u.Daily
	for d := day(since); !d.After(day(u.To)); d = d.AddDate(0, 0, 1) {
		index[d] = len(u.Daily)
		u.Daily = append(u.Daily, store.UsageDay{Date: d.Format("2006-01-02")})
	}
	rows, err := s.r.Query(ctx, usageRuns+`SELECT started_at, inp, outp, cread, cwrite FROM runs`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var at time.Time
		var inp, outp, cread, cwrite int64
		if err := rows.Scan(&at, &inp, &outp, &cread, &cwrite); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[day(at)]; ok {
			d := &u.Daily[i]
			d.Runs++
			d.InputTokens += inp
			d.OutputTokens += outp
			d.CacheReadTokens += cread
			d.CacheWriteTokens += cwrite
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.r.Query(ctx, usageRuns+`SELECT source, count(*), COALESCE(sum(inp), 0), COALESCE(sum(outp), 0), COALESCE(sum(cread), 0), COALESCE(sum(cwrite), 0)
		FROM runs GROUP BY source ORDER BY sum(inp) + sum(outp) DESC`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v store.UsageSource
		if err := rows.Scan(&v.Source, &v.Runs, &v.InputTokens, &v.OutputTokens, &v.CacheReadTokens, &v.CacheWriteTokens); err != nil {
			rows.Close()
			return nil, err
		}
		u.BySource = append(u.BySource, v)
	}
	rows.Close()

	rows, err = s.r.Query(ctx, usageRuns+`SELECT o.id, o.name, o.slug, count(*),
		COALESCE(sum(inp), 0), COALESCE(sum(outp), 0), COALESCE(sum(cread), 0), COALESCE(sum(cwrite), 0)
		FROM runs JOIN orgs o ON o.id = runs.org_id
		GROUP BY o.id, o.name, o.slug ORDER BY sum(inp) + sum(outp) DESC, count(*) DESC`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v store.UsageOrg
		if err := rows.Scan(&v.OrgID, &v.Name, &v.Slug, &v.Runs, &v.InputTokens, &v.OutputTokens, &v.CacheReadTokens, &v.CacheWriteTokens); err != nil {
			rows.Close()
			return nil, err
		}
		u.ByOrg = append(u.ByOrg, v)
	}
	rows.Close()

	rows, err = s.r.Query(ctx, usageRuns+`SELECT a.id, a.name, a.avatar, a.slug, count(*),
		COALESCE(sum(inp), 0), COALESCE(sum(outp), 0), COALESCE(sum(cread), 0), COALESCE(sum(cwrite), 0)
		FROM runs JOIN agents a ON a.id = runs.agent_id
		GROUP BY a.id, a.name, a.avatar, a.slug ORDER BY sum(inp) + sum(outp) DESC, count(*) DESC`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v store.UsageAgent
		if err := rows.Scan(&v.AgentID, &v.Name, &v.Avatar, &v.Slug, &v.Runs, &v.InputTokens, &v.OutputTokens,
			&v.CacheReadTokens, &v.CacheWriteTokens); err != nil {
			rows.Close()
			return nil, err
		}
		u.ByAgent = append(u.ByAgent, v)
	}
	rows.Close()

	rows, err = s.r.Query(ctx, usageRuns+`SELECT runs.connector_id, COALESCE(c.name, ''), COALESCE(c.type, ''),
		runs.runtime, runs.model, count(*), COALESCE(sum(inp), 0), COALESCE(sum(outp), 0), COALESCE(sum(cread), 0), COALESCE(sum(cwrite), 0)
		FROM runs LEFT JOIN connectors c ON c.id = runs.connector_id
		GROUP BY runs.connector_id, c.name, c.type, runs.runtime, runs.model
		ORDER BY sum(inp) + sum(outp) DESC, count(*) DESC`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v store.UsageModel
		if err := rows.Scan(&v.ConnectorID, &v.ConnectorName, &v.ConnectorType, &v.Runtime, &v.Model, &v.Runs,
			&v.InputTokens, &v.OutputTokens, &v.CacheReadTokens, &v.CacheWriteTokens); err != nil {
			rows.Close()
			return nil, err
		}
		u.ByModel = append(u.ByModel, v)
	}
	rows.Close()

	rows, err = s.r.Query(ctx, usageRuns+`SELECT t.id, t.number, t.title, count(*),
		COALESCE(sum(inp), 0), COALESCE(sum(outp), 0), COALESCE(sum(cread), 0), COALESCE(sum(cwrite), 0)
		FROM runs JOIN tasks t ON t.id = runs.task_id
		GROUP BY t.id, t.number, t.title ORDER BY sum(inp) + sum(outp) DESC, count(*) DESC LIMIT 10`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v store.UsageTask
		if err := rows.Scan(&v.TaskID, &v.Number, &v.Title, &v.Runs, &v.InputTokens, &v.OutputTokens, &v.CacheReadTokens, &v.CacheWriteTokens); err != nil {
			return nil, err
		}
		u.TopTasks = append(u.TopTasks, v)
	}
	return u, rows.Err()
}
