-- How full the model's context was on a run's latest turn: context_tokens is that request's prompt
-- (input_tokens counts the whole run), context_window the model's limit, 0 when unknown.
ALTER TABLE agent_runs ADD COLUMN context_tokens BIGINT NOT NULL DEFAULT 0;
ALTER TABLE agent_runs ADD COLUMN context_window BIGINT NOT NULL DEFAULT 0;
