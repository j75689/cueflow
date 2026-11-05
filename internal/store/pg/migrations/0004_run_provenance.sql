-- Record which LLM connector/model (or external agent runtime) each run used, for the Usage page.
ALTER TABLE agent_runs
    ADD COLUMN connector_id UUID REFERENCES connectors(id) ON DELETE SET NULL,
    ADD COLUMN model        TEXT NOT NULL DEFAULT '',
    ADD COLUMN runtime      VARCHAR(16) NOT NULL DEFAULT 'builtin'; -- builtin | acp
ALTER TABLE pipeline_runs
    ADD COLUMN llm_connector_id UUID REFERENCES connectors(id) ON DELETE SET NULL,
    ADD COLUMN model            TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_agent_runs_started ON agent_runs(started_at);
