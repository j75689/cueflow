-- notifier_id is the notifier connector (Telegram, email...) an org's alerts go to: a task that ends
-- waiting for a human (review, a question, an approval, a failed or interrupted run). NULL sends none.
ALTER TABLE orgs ADD COLUMN notifier_id UUID REFERENCES connectors(id) ON DELETE SET NULL;
