-- Threads extras: a topic tag and a link preview card (used by publishers that support them).
ALTER TABLE posts ADD COLUMN topic TEXT NOT NULL DEFAULT '';
ALTER TABLE posts ADD COLUMN link_url TEXT NOT NULL DEFAULT '';
