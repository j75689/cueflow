-- Where drafts made for a task should be published: set on a schedule, copied to the tasks it
-- creates, inherited by subtasks, and applied to the drafts agents write for them.
ALTER TABLE schedules ADD COLUMN publisher_connector_id UUID REFERENCES connectors(id) ON DELETE SET NULL;
ALTER TABLE schedules ADD COLUMN publish_accounts TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE tasks ADD COLUMN publisher_connector_id UUID REFERENCES connectors(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN publish_accounts TEXT[] NOT NULL DEFAULT '{}';
