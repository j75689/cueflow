-- called_as is the tool name the agent saw and called (CueFlow's sanitized, deduplicated name for an
-- MCP tool; "tool" keeps the server's own). result_file is the task file a long result was saved
-- to, or ''.
ALTER TABLE tool_approvals ADD COLUMN called_as VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE tool_approvals ADD COLUMN result_file VARCHAR(200) NOT NULL DEFAULT '';
