-- Media bytes may live outside the database (CUEFLOW_MEDIA_STORAGE). "storage" says where.
ALTER TABLE media ALTER COLUMN data DROP NOT NULL;
ALTER TABLE media ADD COLUMN storage VARCHAR(16) NOT NULL DEFAULT 'db';
