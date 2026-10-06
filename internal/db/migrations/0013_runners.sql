-- Runners: machines that register with CueFlow and pull agent work (like a CI self-hosted runner).
CREATE TABLE runners (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name             VARCHAR(64) NOT NULL,
    labels           TEXT[] NOT NULL DEFAULT '{}',
    org_ids          UUID[] NOT NULL DEFAULT '{}',      -- empty = every org
    token_hash       BYTEA NOT NULL UNIQUE,             -- SHA-256; the plaintext is shown once
    is_active        BOOLEAN NOT NULL DEFAULT TRUE,
    max_concurrency  INT NOT NULL DEFAULT 1,
    version          VARCHAR(32) NOT NULL DEFAULT '',
    platform         VARCHAR(64) NOT NULL DEFAULT '',
    last_seen_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE runner_registrations (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash  BYTEA NOT NULL UNIQUE,
    labels      TEXT[] NOT NULL DEFAULT '{}',
    org_ids     UUID[] NOT NULL DEFAULT '{}',
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ
);

CREATE TABLE runner_jobs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       UUID NOT NULL REFERENCES orgs(id),
    task_id      UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_run_id UUID REFERENCES agent_runs(id) ON DELETE SET NULL,
    labels       TEXT[] NOT NULL DEFAULT '{}',
    status       VARCHAR(16) NOT NULL DEFAULT 'queued',  -- queued | running | done | failed | canceled | expired
    runner_id    UUID REFERENCES runners(id) ON DELETE SET NULL,
    lease_until  TIMESTAMPTZ,
    payload      JSONB NOT NULL,
    result       JSONB,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ
);
CREATE INDEX idx_runner_jobs_queue ON runner_jobs(created_at) WHERE status = 'queued';
CREATE INDEX idx_runner_jobs_task ON runner_jobs(task_id, created_at DESC);
