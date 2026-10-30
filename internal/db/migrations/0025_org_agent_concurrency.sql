-- agent_concurrency is how many of an org's tasks the built-in crew may work at once, across every
-- instance. NULL uses CUEFLOW_AGENT_CONCURRENCY.
ALTER TABLE orgs ADD COLUMN agent_concurrency INT CHECK (agent_concurrency BETWEEN 1 AND 64);
