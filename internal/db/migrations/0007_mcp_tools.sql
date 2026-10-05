-- External MCP tool servers for agents (connector kind "tools").

-- Per tool: allow | ask | deny. Tools missing here use the default (read-only allow, else ask).
ALTER TABLE connectors ADD COLUMN tool_permissions JSONB NOT NULL DEFAULT '{}';

-- Tool servers an agent may use.
ALTER TABLE agents ADD COLUMN tool_connector_ids UUID[] NOT NULL DEFAULT '{}';

-- Calls to "ask" tools wait here for a human decision.
CREATE TABLE tool_approvals (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id      UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_id     UUID REFERENCES agents(id) ON DELETE SET NULL,
    connector_id UUID REFERENCES connectors(id) ON DELETE SET NULL,
    server_name  TEXT NOT NULL,
    tool         TEXT NOT NULL,
    arguments    JSONB NOT NULL DEFAULT '{}',
    status       VARCHAR(16) NOT NULL DEFAULT 'pending',  -- pending | approved | rejected
    result       TEXT NOT NULL DEFAULT '',
    is_error     BOOLEAN NOT NULL DEFAULT FALSE,
    note         TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    decided_at   TIMESTAMPTZ
);
CREATE INDEX idx_tool_approvals_task ON tool_approvals(task_id, created_at);
