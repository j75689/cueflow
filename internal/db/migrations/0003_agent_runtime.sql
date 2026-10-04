-- An agent can be run by an external ACP agent (Claude Code, Codex, Gemini CLI...) described by a
-- runtime connector. NULL keeps the built-in LLM tool loop.
ALTER TABLE agents ADD COLUMN runtime_connector_id UUID REFERENCES connectors(id) ON DELETE SET NULL;
