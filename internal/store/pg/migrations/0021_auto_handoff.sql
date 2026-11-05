-- auto_handoff lets an agent's run that handed off (with a note) continue in a fresh run without
-- waiting for a human. outcome records how a run ended (complete, ask, handoff, limit, ...), from
-- which the crew counts consecutive automatic hand-offs; '' for runs before this column.
ALTER TABLE agents ADD COLUMN auto_handoff BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE agent_runs ADD COLUMN outcome VARCHAR(16) NOT NULL DEFAULT '';
