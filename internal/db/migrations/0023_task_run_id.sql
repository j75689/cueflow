-- run_id is the run the task's latest claim minted (agent_runs.id; no FK, the run row is written
-- after the claim). The run extends the lease and finishes the task only while it is still its own,
-- so a run that stalled past its lease while the task was claimed again can't keep working it.
ALTER TABLE tasks ADD COLUMN run_id UUID;
