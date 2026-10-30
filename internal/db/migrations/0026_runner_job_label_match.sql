-- label_match is how a job's labels match a runner's: 'all' (the runner has every label, as before) or
-- 'any' (one in common is enough). A job with no labels matches any runner either way.
ALTER TABLE runner_jobs ADD COLUMN label_match TEXT NOT NULL DEFAULT 'all' CHECK (label_match IN ('all', 'any'));
