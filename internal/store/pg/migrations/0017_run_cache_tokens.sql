-- How much of a run's input_tokens the provider read from or wrote to its prompt cache. input_tokens
-- stays the whole prompt; these say how much of it was billed at the cache rates.
ALTER TABLE agent_runs ADD COLUMN cache_read_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_runs ADD COLUMN cache_write_tokens BIGINT NOT NULL DEFAULT 0;
