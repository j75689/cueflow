-- Orgs: each org has its own agents, board, schedules, pipelines, review queue and runs.
-- Connectors, source items and media stay global and are shared by every org.

CREATE TABLE orgs (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name             VARCHAR(64) NOT NULL,
    slug             VARCHAR(64) NOT NULL UNIQUE,
    next_task_number BIGINT NOT NULL DEFAULT 1,   -- task numbers count per org (#1, #2, ...)
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO orgs (name, slug, next_task_number)
VALUES ('Default', 'default', COALESCE((SELECT max(number) FROM tasks), 0) + 1);

ALTER TABLE agents ADD COLUMN org_id UUID REFERENCES orgs(id);
ALTER TABLE schedules ADD COLUMN org_id UUID REFERENCES orgs(id);
ALTER TABLE tasks ADD COLUMN org_id UUID REFERENCES orgs(id);
ALTER TABLE pipelines ADD COLUMN org_id UUID REFERENCES orgs(id);
ALTER TABLE pipeline_runs ADD COLUMN org_id UUID REFERENCES orgs(id);
ALTER TABLE posts ADD COLUMN org_id UUID REFERENCES orgs(id);
ALTER TABLE agent_runs ADD COLUMN org_id UUID REFERENCES orgs(id);

UPDATE agents SET org_id = (SELECT id FROM orgs);
UPDATE schedules SET org_id = (SELECT id FROM orgs);
UPDATE tasks SET org_id = (SELECT id FROM orgs);
UPDATE pipelines SET org_id = (SELECT id FROM orgs);
UPDATE pipeline_runs SET org_id = (SELECT id FROM orgs);
UPDATE posts SET org_id = (SELECT id FROM orgs);
UPDATE agent_runs SET org_id = (SELECT id FROM orgs);

ALTER TABLE agents ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE schedules ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE tasks ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE pipelines ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE pipeline_runs ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE posts ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE agent_runs ALTER COLUMN org_id SET NOT NULL;

-- Agent slugs (@mentions) are unique within an org.
ALTER TABLE agents DROP CONSTRAINT agents_slug_key;
ALTER TABLE agents ADD CONSTRAINT agents_org_slug_key UNIQUE (org_id, slug);

-- Task numbers come from orgs.next_task_number instead of a global sequence.
ALTER TABLE tasks DROP CONSTRAINT tasks_number_key;
ALTER TABLE tasks ALTER COLUMN number DROP DEFAULT;
DROP SEQUENCE IF EXISTS tasks_number_seq;
ALTER TABLE tasks ADD CONSTRAINT tasks_org_number_key UNIQUE (org_id, number);

CREATE INDEX idx_agents_org ON agents(org_id);
CREATE INDEX idx_schedules_org ON schedules(org_id);
CREATE INDEX idx_tasks_org_status ON tasks(org_id, status, position);
CREATE INDEX idx_pipelines_org ON pipelines(org_id);
CREATE INDEX idx_pipeline_runs_org ON pipeline_runs(org_id, started_at DESC);
CREATE INDEX idx_posts_org ON posts(org_id, created_at DESC);
CREATE INDEX idx_agent_runs_org ON agent_runs(org_id, started_at DESC);
