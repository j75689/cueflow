-- The PostgreSQL schema as of its migration 0027, in SQLite's types, without the retired posts,
-- pipelines, pipeline_runs and audit_logs tables. A later migration ships as a pair: the same NNNN_
-- prefix here and in store/pg/migrations.
--
-- Mapping: UUID is 36-character lowercase TEXT; TIMESTAMPTZ is TEXT in UTC, fixed width
-- (2006-01-02T15:04:05.000000000Z), so comparing the text compares the times; TEXT[]/UUID[] and JSONB
-- are JSON in TEXT; BOOLEAN is INTEGER 0/1; BYTEA is BLOB. The store passes ids and times itself; the
-- defaults below keep the columns' PostgreSQL defaults for rows written without them.

CREATE TABLE connectors (
    id               TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    name             TEXT NOT NULL,
    kind             TEXT NOT NULL,
    type             TEXT NOT NULL,
    config           TEXT NOT NULL DEFAULT '{}',
    secrets          BLOB,
    state            TEXT NOT NULL DEFAULT '{}',
    is_active        INTEGER NOT NULL DEFAULT 1,
    last_status      TEXT,
    last_error       TEXT,
    last_checked_at  TEXT,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    tool_permissions TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_connectors_kind ON connectors (kind);

CREATE TABLE orgs (
    id                TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    name              TEXT NOT NULL,
    slug              TEXT NOT NULL UNIQUE,
    next_task_number  INTEGER NOT NULL DEFAULT 1,
    created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    notifier_id       TEXT REFERENCES connectors(id) ON DELETE SET NULL,
    agent_concurrency INTEGER CHECK (agent_concurrency BETWEEN 1 AND 64)
);
-- The default org, as 0011_orgs.sql creates it.
INSERT INTO orgs (name, slug) VALUES ('Default', 'default');

CREATE TABLE agents (
    id                   TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    name                 TEXT NOT NULL,
    slug                 TEXT NOT NULL,
    avatar               TEXT NOT NULL DEFAULT '🤖',
    role                 TEXT NOT NULL DEFAULT 'custom',
    description          TEXT NOT NULL DEFAULT '',
    instructions         TEXT NOT NULL DEFAULT '',
    capabilities         TEXT NOT NULL DEFAULT '[]',
    llm_connector_id     TEXT REFERENCES connectors(id) ON DELETE SET NULL,
    search_connector_id  TEXT REFERENCES connectors(id) ON DELETE SET NULL,
    source_connector_ids TEXT NOT NULL DEFAULT '[]',
    language             TEXT NOT NULL DEFAULT 'en',
    platforms            TEXT NOT NULL DEFAULT '["threads"]',
    max_turns            INTEGER NOT NULL DEFAULT 12,
    is_active            INTEGER NOT NULL DEFAULT 1,
    created_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    runtime_connector_id TEXT REFERENCES connectors(id) ON DELETE SET NULL,
    tool_connector_ids   TEXT NOT NULL DEFAULT '[]',
    org_id               TEXT NOT NULL REFERENCES orgs(id),
    auto_handoff         INTEGER NOT NULL DEFAULT 0,
    auto_recover         INTEGER NOT NULL DEFAULT 0 CHECK (auto_recover BETWEEN 0 AND 10),
    CONSTRAINT agents_org_slug_key UNIQUE (org_id, slug)
);
CREATE INDEX idx_agents_org ON agents (org_id);

CREATE TABLE agent_memories (
    id         TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    content    TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
);
CREATE INDEX idx_agent_memories_agent ON agent_memories (agent_id, created_at DESC);

CREATE TABLE schedules (
    id                     TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    name                   TEXT NOT NULL,
    cron_expr              TEXT NOT NULL,
    timezone               TEXT NOT NULL DEFAULT 'UTC',
    is_active              INTEGER NOT NULL DEFAULT 1,
    assignee_agent_id      TEXT REFERENCES agents(id) ON DELETE SET NULL,
    task_title             TEXT NOT NULL,
    task_description       TEXT NOT NULL DEFAULT '',
    priority               TEXT NOT NULL DEFAULT 'normal',
    created_by             TEXT NOT NULL DEFAULT 'human',
    creator_agent_id       TEXT REFERENCES agents(id) ON DELETE SET NULL,
    last_run_at            TEXT,
    created_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    publisher_connector_id TEXT REFERENCES connectors(id) ON DELETE SET NULL,
    publish_accounts       TEXT NOT NULL DEFAULT '[]',
    org_id                 TEXT NOT NULL REFERENCES orgs(id),
    next_run_at            TEXT
);
CREATE INDEX idx_schedules_org ON schedules (org_id);

CREATE TABLE tasks (
    id                     TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    number                 INTEGER NOT NULL,
    title                  TEXT NOT NULL,
    description            TEXT NOT NULL DEFAULT '',
    status                 TEXT NOT NULL DEFAULT 'BACKLOG'
                           CHECK (status IN ('BACKLOG','TODO','IN_PROGRESS','BLOCKED','WAITING_HUMAN','REVIEW','DONE','CANCELED')),
    priority               TEXT NOT NULL DEFAULT 'normal' CHECK (priority IN ('low','normal','high','urgent')),
    labels                 TEXT NOT NULL DEFAULT '[]',
    assignee_agent_id      TEXT REFERENCES agents(id) ON DELETE SET NULL,
    created_by             TEXT NOT NULL DEFAULT 'human',
    creator_agent_id       TEXT REFERENCES agents(id) ON DELETE SET NULL,
    parent_id              TEXT REFERENCES tasks(id) ON DELETE CASCADE,
    schedule_id            TEXT REFERENCES schedules(id) ON DELETE SET NULL,
    result                 TEXT NOT NULL DEFAULT '',
    due_at                 TEXT,
    position               REAL NOT NULL DEFAULT 0,
    lease_until            TEXT,
    attempts               INTEGER NOT NULL DEFAULT 0,
    version                INTEGER NOT NULL DEFAULT 1,
    created_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at             TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    started_at             TEXT,
    completed_at           TEXT,
    publisher_connector_id TEXT REFERENCES connectors(id) ON DELETE SET NULL,
    publish_accounts       TEXT NOT NULL DEFAULT '[]',
    org_id                 TEXT NOT NULL REFERENCES orgs(id),
    run_id                 TEXT,
    retry_at               TEXT,
    recover_reset_at       TEXT,
    CONSTRAINT tasks_org_number_key UNIQUE (org_id, number)
);
CREATE INDEX idx_tasks_assignee ON tasks (assignee_agent_id);
CREATE INDEX idx_tasks_org_status ON tasks (org_id, status, position);
CREATE INDEX idx_tasks_parent ON tasks (parent_id);
CREATE INDEX idx_tasks_status ON tasks (status, position);

CREATE TABLE task_messages (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    seq         INTEGER NOT NULL, -- PostgreSQL's BIGSERIAL: the store writes max(seq) + 1
    task_id     TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    author_type TEXT NOT NULL,
    agent_id    TEXT REFERENCES agents(id) ON DELETE SET NULL,
    kind        TEXT NOT NULL DEFAULT 'message',
    content     TEXT NOT NULL DEFAULT '',
    data        TEXT,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
);
CREATE INDEX idx_task_messages_task ON task_messages (task_id, seq);
CREATE INDEX idx_task_messages_seq ON task_messages (seq); -- the max(seq) + 1 on insert

CREATE TABLE agent_runs (
    id                 TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    task_id            TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_id           TEXT REFERENCES agents(id) ON DELETE SET NULL,
    status             TEXT NOT NULL DEFAULT 'RUNNING',
    turns              INTEGER NOT NULL DEFAULT 0,
    tool_calls         INTEGER NOT NULL DEFAULT 0,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    error              TEXT NOT NULL DEFAULT '',
    started_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    finished_at        TEXT,
    connector_id       TEXT REFERENCES connectors(id) ON DELETE SET NULL,
    model              TEXT NOT NULL DEFAULT '',
    runtime            TEXT NOT NULL DEFAULT 'builtin',
    org_id             TEXT NOT NULL REFERENCES orgs(id),
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    context_tokens     INTEGER NOT NULL DEFAULT 0,
    context_window     INTEGER NOT NULL DEFAULT 0,
    outcome            TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_agent_runs_org ON agent_runs (org_id, started_at DESC);
CREATE INDEX idx_agent_runs_started ON agent_runs (started_at);
CREATE INDEX idx_agent_runs_task ON agent_runs (task_id, started_at DESC);

CREATE TABLE agent_tokens (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    agent_id     TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   BLOB NOT NULL UNIQUE,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    last_used_at TEXT
);
CREATE INDEX idx_agent_tokens_agent ON agent_tokens (agent_id);

CREATE TABLE run_tokens (
    token_hash BLOB PRIMARY KEY,
    task_id    TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    outcome    TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    expires_at TEXT NOT NULL,
    run_id     TEXT
);
CREATE INDEX idx_run_tokens_expires ON run_tokens (expires_at);

CREATE TABLE tool_approvals (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    task_id      TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_id     TEXT REFERENCES agents(id) ON DELETE SET NULL,
    connector_id TEXT REFERENCES connectors(id) ON DELETE SET NULL,
    server_name  TEXT NOT NULL,
    tool         TEXT NOT NULL,
    arguments    TEXT NOT NULL DEFAULT '{}',
    status       TEXT NOT NULL DEFAULT 'pending',
    result       TEXT NOT NULL DEFAULT '',
    is_error     INTEGER NOT NULL DEFAULT 0,
    note         TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    decided_at   TEXT,
    called_as    TEXT NOT NULL DEFAULT '',
    result_file  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_tool_approvals_task ON tool_approvals (task_id, created_at);

CREATE TABLE media (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    mime_type    TEXT NOT NULL,
    data         BLOB,
    size_bytes   INTEGER NOT NULL,
    source       TEXT NOT NULL,
    prompt       TEXT NOT NULL DEFAULT '',
    connector_id TEXT REFERENCES connectors(id) ON DELETE SET NULL,
    created_by   TEXT NOT NULL DEFAULT 'human',
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    storage      TEXT NOT NULL DEFAULT 'db'
);
CREATE INDEX idx_media_created ON media (created_at DESC);

CREATE TABLE clock (
    name        TEXT PRIMARY KEY,
    next_run_at TEXT NOT NULL
);

CREATE TABLE runners (
    id              TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    name            TEXT NOT NULL,
    labels          TEXT NOT NULL DEFAULT '[]',
    org_ids         TEXT NOT NULL DEFAULT '[]',
    token_hash      BLOB NOT NULL UNIQUE,
    is_active       INTEGER NOT NULL DEFAULT 1,
    max_concurrency INTEGER NOT NULL DEFAULT 1,
    version         TEXT NOT NULL DEFAULT '',
    platform        TEXT NOT NULL DEFAULT '',
    last_seen_at    TEXT,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
);

CREATE TABLE runner_registrations (
    id         TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    token_hash BLOB NOT NULL UNIQUE,
    labels     TEXT NOT NULL DEFAULT '[]',
    org_ids    TEXT NOT NULL DEFAULT '[]',
    expires_at TEXT NOT NULL,
    used_at    TEXT
);

CREATE TABLE runner_jobs (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    org_id       TEXT NOT NULL REFERENCES orgs(id),
    task_id      TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_run_id TEXT REFERENCES agent_runs(id) ON DELETE SET NULL,
    labels       TEXT NOT NULL DEFAULT '[]',
    status       TEXT NOT NULL DEFAULT 'queued',
    runner_id    TEXT REFERENCES runners(id) ON DELETE SET NULL,
    lease_until  TEXT,
    payload      TEXT NOT NULL,
    result       TEXT,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    started_at   TEXT,
    finished_at  TEXT,
    label_match  TEXT NOT NULL DEFAULT 'all' CHECK (label_match IN ('all', 'any'))
);
CREATE INDEX idx_runner_jobs_queue ON runner_jobs (created_at) WHERE status = 'queued';
CREATE INDEX idx_runner_jobs_task ON runner_jobs (task_id, created_at DESC);

CREATE TABLE task_files (
    id         TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    task_id    TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    mime_type  TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    data       BLOB,
    storage    TEXT NOT NULL DEFAULT 'db',
    created_by TEXT NOT NULL DEFAULT 'agent',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    UNIQUE (task_id, name)
);

CREATE TABLE deliverables (
    id                  TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    org_id              TEXT NOT NULL REFERENCES orgs(id),
    task_id             TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    kind                TEXT NOT NULL,
    title               TEXT NOT NULL DEFAULT '',
    payload             TEXT NOT NULL DEFAULT '{}',
    status              TEXT NOT NULL DEFAULT 'DRAFT'
                        CHECK (status IN ('DRAFT','PENDING_REVIEW','APPROVED','REJECTED','QUEUED','DONE','FAILED')),
    review_note         TEXT NOT NULL DEFAULT '',
    error_message       TEXT NOT NULL DEFAULT '',
    action_connector_id TEXT REFERENCES connectors(id) ON DELETE SET NULL,
    external_ids        TEXT NOT NULL DEFAULT '{}',
    result              TEXT NOT NULL DEFAULT '{}',
    attempts            INTEGER NOT NULL DEFAULT 0,
    lease_until         TEXT,
    version             INTEGER NOT NULL DEFAULT 1,
    created_by          TEXT NOT NULL DEFAULT '',
    acted_at            TEXT,
    created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    updated_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
);
CREATE INDEX idx_deliverables_created ON deliverables (created_at DESC);
CREATE INDEX idx_deliverables_org_status ON deliverables (org_id, status);
CREATE INDEX idx_deliverables_task ON deliverables (task_id);

CREATE TABLE deliverable_audit_logs (
    id             TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    seq            INTEGER NOT NULL, -- PostgreSQL's BIGSERIAL: the store writes max(seq) + 1
    deliverable_id TEXT NOT NULL REFERENCES deliverables(id) ON DELETE CASCADE,
    action         TEXT NOT NULL,
    actor          TEXT NOT NULL,
    from_status    TEXT NOT NULL DEFAULT '',
    to_status      TEXT NOT NULL DEFAULT '',
    note           TEXT NOT NULL DEFAULT '',
    payload_diff   TEXT,
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'))
);
CREATE INDEX idx_deliverable_audit_logs_item ON deliverable_audit_logs (deliverable_id, seq);
CREATE INDEX idx_deliverable_audit_logs_seq ON deliverable_audit_logs (seq); -- the max(seq) + 1 on insert

CREATE TABLE source_items (
    id           TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))),
    connector_id TEXT NOT NULL REFERENCES connectors(id) ON DELETE CASCADE,
    external_id  TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    url          TEXT NOT NULL DEFAULT '',
    author       TEXT NOT NULL DEFAULT '',
    content      TEXT NOT NULL DEFAULT '',
    published_at TEXT,
    fetched_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now')),
    consumed_at  TEXT,
    UNIQUE (connector_id, external_id)
);
CREATE INDEX idx_source_items_connector_fetched ON source_items (connector_id, fetched_at DESC);
