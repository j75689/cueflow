-- run_id is the agent run a run token serves, so a tool call through the token is refused once the
-- run has lost its task's claim (tasks.run_id), on whichever instance serves it.
ALTER TABLE run_tokens ADD COLUMN run_id UUID;
