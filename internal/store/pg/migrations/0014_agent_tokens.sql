-- Credentials for external agents that drive their own work over MCP (cfa_... tokens).
CREATE TABLE agent_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id     UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    name         VARCHAR(64) NOT NULL,
    token_hash   BYTEA NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ
);
CREATE INDEX idx_agent_tokens_agent ON agent_tokens(agent_id);
