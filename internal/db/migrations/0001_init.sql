-- CueFlow initial schema.

-- Plugins/integrations. Non-secret settings live in `config` (readable, shown in the UI);
-- secret fields (API keys, passwords) are AES-256-GCM encrypted into `secrets`.
CREATE TABLE connectors (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(64)  NOT NULL,
    kind            VARCHAR(16)  NOT NULL,  -- publisher | source | search | llm | notifier
    type            VARCHAR(32)  NOT NULL,  -- postiz_mcp | rss | imap | searxng | tavily | openai | anthropic | smtp ...
    config          JSONB        NOT NULL DEFAULT '{}',
    secrets         BYTEA,
    state           JSONB        NOT NULL DEFAULT '{}', -- plugin cursor state (e.g. IMAP last UID)
    is_active       BOOLEAN      NOT NULL DEFAULT TRUE,
    last_status     VARCHAR(16),
    last_error      TEXT,
    last_checked_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_connectors_kind ON connectors(kind);

-- Raw material pulled by source connectors. UNIQUE(connector_id, external_id) gives delta fetching:
-- re-fetching a feed only inserts entries whose GUID/Message-ID has not been seen.
CREATE TABLE source_items (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connector_id UUID NOT NULL REFERENCES connectors(id) ON DELETE CASCADE,
    external_id  TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    url          TEXT NOT NULL DEFAULT '',
    author       TEXT NOT NULL DEFAULT '',
    content      TEXT NOT NULL DEFAULT '',
    published_at TIMESTAMPTZ,
    fetched_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    consumed_at  TIMESTAMPTZ,
    UNIQUE (connector_id, external_id)
);
CREATE INDEX idx_source_items_connector_fetched ON source_items(connector_id, fetched_at DESC);

-- Scheduled agent jobs ("every day 08:00: read these feeds, write Threads + IG drafts").
CREATE TABLE pipelines (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                   VARCHAR(128) NOT NULL,
    description            TEXT   NOT NULL DEFAULT '',
    is_active              BOOLEAN NOT NULL DEFAULT TRUE,
    cron_expr              VARCHAR(64) NOT NULL,
    timezone               VARCHAR(64) NOT NULL DEFAULT 'UTC',
    topic                  TEXT   NOT NULL DEFAULT '',
    instructions           TEXT   NOT NULL DEFAULT '',
    language               VARCHAR(16) NOT NULL DEFAULT 'zh-TW',
    platforms              TEXT[] NOT NULL,
    llm_connector_id       UUID REFERENCES connectors(id) ON DELETE SET NULL,
    source_connector_ids   UUID[] NOT NULL DEFAULT '{}',
    search_connector_id    UUID REFERENCES connectors(id) ON DELETE SET NULL,
    search_queries         TEXT[] NOT NULL DEFAULT '{}',
    publisher_connector_id UUID REFERENCES connectors(id) ON DELETE SET NULL,
    notifier_connector_id  UUID REFERENCES connectors(id) ON DELETE SET NULL,
    lookback_hours         INT NOT NULL DEFAULT 24,
    max_items              INT NOT NULL DEFAULT 20,
    last_run_at            TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE pipeline_runs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_id UUID NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    trigger     VARCHAR(16) NOT NULL,              -- cron | manual
    status      VARCHAR(16) NOT NULL DEFAULT 'RUNNING' CHECK (status IN ('RUNNING','SUCCEEDED','FAILED')),
    started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    error       TEXT NOT NULL DEFAULT '',
    stats       JSONB NOT NULL DEFAULT '{}',
    log         JSONB NOT NULL DEFAULT '[]'
);
CREATE INDEX idx_pipeline_runs_pipeline ON pipeline_runs(pipeline_id, started_at DESC);
CREATE INDEX idx_pipeline_runs_started ON pipeline_runs(started_at DESC);

-- Posts and their review state machine.
CREATE TABLE posts (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id               UUID,            -- variants generated for the same topic share a group
    pipeline_id            UUID REFERENCES pipelines(id) ON DELETE SET NULL,
    run_id                 UUID REFERENCES pipeline_runs(id) ON DELETE SET NULL,
    publisher_connector_id UUID REFERENCES connectors(id) ON DELETE SET NULL,
    title                  VARCHAR(255) NOT NULL DEFAULT '',
    content                TEXT   NOT NULL,
    thread_parts           TEXT[] NOT NULL DEFAULT '{}',
    hashtags               TEXT[] NOT NULL DEFAULT '{}',
    image_prompt           TEXT   NOT NULL DEFAULT '',
    platforms              TEXT[] NOT NULL,
    media_urls             TEXT[] NOT NULL DEFAULT '{}',
    status                 VARCHAR(32) NOT NULL DEFAULT 'DRAFT' CHECK (status IN
                             ('DRAFT','PENDING_REVIEW','APPROVED','REJECTED','QUEUED','PUBLISHED','FAILED')),
    review_note            TEXT   NOT NULL DEFAULT '',
    source_context         JSONB  NOT NULL DEFAULT '{}',
    error_message          TEXT   NOT NULL DEFAULT '',
    external_post_ids      JSONB  NOT NULL DEFAULT '{}',
    attempts               INT    NOT NULL DEFAULT 0,
    lease_until            TIMESTAMPTZ,     -- dispatcher lease; an expired lease means the worker died mid-publish
    version                INT    NOT NULL DEFAULT 1,
    scheduled_at           TIMESTAMPTZ,
    published_at           TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_posts_status ON posts(status);
CREATE INDEX idx_posts_created_at ON posts(created_at DESC);
CREATE INDEX idx_posts_group ON posts(group_id);

CREATE TABLE audit_logs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    seq          BIGSERIAL NOT NULL,    -- insertion order; entries written in one transaction share created_at
    post_id      UUID NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    action       VARCHAR(32) NOT NULL,  -- GENERATED, CREATED, EDITED, SUBMITTED, APPROVED, REJECTED, QUEUED, DISPATCHED, PUBLISHED, FAILED, RETRIED
    actor        VARCHAR(64) NOT NULL,  -- system_agent | system_worker | human_reviewer
    from_status  VARCHAR(32) NOT NULL DEFAULT '',
    to_status    VARCHAR(32) NOT NULL DEFAULT '',
    note         TEXT NOT NULL DEFAULT '',
    payload_diff JSONB,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_audit_logs_post ON audit_logs(post_id, seq);
