-- State for the external clock (Cloud Scheduler calling /internal/tick): when each cron-driven thing
-- fires next. In the local deployment the in-process cron is used and these stay unused.
ALTER TABLE schedules ADD COLUMN next_run_at TIMESTAMPTZ;
ALTER TABLE pipelines ADD COLUMN next_run_at TIMESTAMPTZ;

CREATE TABLE clock (
    name        VARCHAR(32) PRIMARY KEY,   -- e.g. 'digest'
    next_run_at TIMESTAMPTZ NOT NULL
);

-- Per-run MCP tool servers must work from any instance: the run's credential lives in the database.
CREATE TABLE run_tokens (
    token_hash  BYTEA PRIMARY KEY,         -- SHA-256 of the token in the run's MCP URL
    task_id     UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_id    UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    outcome     JSONB,                     -- set by a terminal tool call: {"kind","text"}
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_run_tokens_expires ON run_tokens(expires_at);
