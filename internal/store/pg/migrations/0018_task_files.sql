-- Task files: an agent's working storage for one task (notes, large tool results, hand-off notes).
-- Bytes live where media bytes do ("storage", as on media); they are never served by /media/{id}.
-- A subtask may read its ancestors' files but writes only its own (enforced by the store's API).
CREATE TABLE task_files (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id     UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    name        VARCHAR(200) NOT NULL,
    mime_type   VARCHAR(64) NOT NULL,
    size_bytes  BIGINT NOT NULL,
    data        BYTEA,
    storage     VARCHAR(16) NOT NULL DEFAULT 'db',
    created_by  VARCHAR(64) NOT NULL DEFAULT 'agent',  -- human | agent slug
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (task_id, name)
);
