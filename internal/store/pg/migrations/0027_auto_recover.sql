-- auto_recover is how many failed runs in a row (transient failures only) an agent's task goes back
-- to TODO on its own, after a backoff, instead of waiting for a human. 0 = off.
ALTER TABLE agents ADD COLUMN auto_recover INT NOT NULL DEFAULT 0 CHECK (auto_recover BETWEEN 0 AND 10);
-- retry_at holds a recovered task in TODO until then; NULL = claimable now. Any status change clears it.
ALTER TABLE tasks ADD COLUMN retry_at TIMESTAMPTZ;
-- recover_reset_at is when a human last moved the task (a status change, or Run): auto-recover counts no run
-- that started before it. NULL = never.
ALTER TABLE tasks ADD COLUMN recover_reset_at TIMESTAMPTZ;
