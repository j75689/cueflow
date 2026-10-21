-- The task claim (tasks.attempts at claim time) a run works under, so a handed-over run resumed by
-- a later delivery keeps fencing the task's lease by its own claim.
ALTER TABLE agent_runs ADD COLUMN attempt INT NOT NULL DEFAULT 0;
