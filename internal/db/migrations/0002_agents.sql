-- Agent team: roles, memories, a kanban task board with threads, agent runs, and recurring schedules.

CREATE TABLE agents (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                 VARCHAR(64) NOT NULL,
    slug                 VARCHAR(64) NOT NULL UNIQUE,     -- used for @mentions and delegation
    avatar               VARCHAR(16) NOT NULL DEFAULT '🤖',
    role                 VARCHAR(16) NOT NULL DEFAULT 'custom',
    description          TEXT NOT NULL DEFAULT '',        -- shown to teammates for delegation
    instructions         TEXT NOT NULL DEFAULT '',        -- persona / standing orders
    capabilities         TEXT[] NOT NULL DEFAULT '{}',    -- tool groups this agent may use
    llm_connector_id     UUID REFERENCES connectors(id) ON DELETE SET NULL,
    search_connector_id  UUID REFERENCES connectors(id) ON DELETE SET NULL,
    source_connector_ids UUID[] NOT NULL DEFAULT '{}',
    language             VARCHAR(16) NOT NULL DEFAULT 'en',
    platforms            TEXT[] NOT NULL DEFAULT '{threads}',
    max_turns            INT NOT NULL DEFAULT 12,
    is_active            BOOLEAN NOT NULL DEFAULT TRUE,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE agent_memories (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id   UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    content    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_agent_memories_agent ON agent_memories(agent_id, created_at DESC);

CREATE TABLE schedules (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name              VARCHAR(128) NOT NULL,
    cron_expr         VARCHAR(64) NOT NULL,
    timezone          VARCHAR(64) NOT NULL DEFAULT 'UTC',
    is_active         BOOLEAN NOT NULL DEFAULT TRUE,
    assignee_agent_id UUID REFERENCES agents(id) ON DELETE SET NULL,
    task_title        VARCHAR(255) NOT NULL,
    task_description  TEXT NOT NULL DEFAULT '',
    priority          VARCHAR(8) NOT NULL DEFAULT 'normal',
    created_by        VARCHAR(8) NOT NULL DEFAULT 'human',
    creator_agent_id  UUID REFERENCES agents(id) ON DELETE SET NULL,
    last_run_at       TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE tasks (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    number            BIGSERIAL NOT NULL UNIQUE,
    title             VARCHAR(255) NOT NULL,
    description       TEXT NOT NULL DEFAULT '',
    status            VARCHAR(16) NOT NULL DEFAULT 'BACKLOG' CHECK (status IN
                        ('BACKLOG','TODO','IN_PROGRESS','BLOCKED','WAITING_HUMAN','REVIEW','DONE','CANCELED')),
    priority          VARCHAR(8) NOT NULL DEFAULT 'normal' CHECK (priority IN ('low','normal','high','urgent')),
    labels            TEXT[] NOT NULL DEFAULT '{}',
    assignee_agent_id UUID REFERENCES agents(id) ON DELETE SET NULL,
    created_by        VARCHAR(8) NOT NULL DEFAULT 'human',
    creator_agent_id  UUID REFERENCES agents(id) ON DELETE SET NULL,
    parent_id         UUID REFERENCES tasks(id) ON DELETE CASCADE,
    schedule_id       UUID REFERENCES schedules(id) ON DELETE SET NULL,
    result            TEXT NOT NULL DEFAULT '',
    due_at            TIMESTAMPTZ,
    position          DOUBLE PRECISION NOT NULL DEFAULT 0,
    lease_until       TIMESTAMPTZ,      -- agent run lease; renewed while the run is alive
    attempts          INT NOT NULL DEFAULT 0,
    version           INT NOT NULL DEFAULT 1,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at        TIMESTAMPTZ,
    completed_at      TIMESTAMPTZ
);
CREATE INDEX idx_tasks_status ON tasks(status, position);
CREATE INDEX idx_tasks_parent ON tasks(parent_id);
CREATE INDEX idx_tasks_assignee ON tasks(assignee_agent_id);

CREATE TABLE task_messages (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    seq         BIGSERIAL NOT NULL,
    task_id     UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    author_type VARCHAR(8) NOT NULL,   -- human | agent | system | tool
    agent_id    UUID REFERENCES agents(id) ON DELETE SET NULL,
    kind        VARCHAR(16) NOT NULL DEFAULT 'message', -- message | tool_call | tool_result | status | question | result
    content     TEXT NOT NULL DEFAULT '',
    data        JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_task_messages_task ON task_messages(task_id, seq);

CREATE TABLE agent_runs (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id       UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_id      UUID REFERENCES agents(id) ON DELETE SET NULL,
    status        VARCHAR(16) NOT NULL DEFAULT 'RUNNING',
    turns         INT NOT NULL DEFAULT 0,
    tool_calls    INT NOT NULL DEFAULT 0,
    input_tokens  BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    error         TEXT NOT NULL DEFAULT '',
    started_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at   TIMESTAMPTZ
);
CREATE INDEX idx_agent_runs_task ON agent_runs(task_id, started_at DESC);

ALTER TABLE posts ADD COLUMN task_id UUID REFERENCES tasks(id) ON DELETE SET NULL;
CREATE INDEX idx_posts_task ON posts(task_id);
