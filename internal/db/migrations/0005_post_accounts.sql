-- Accounts a post goes to, for publishers that target individual accounts (e.g. CueSocial).
ALTER TABLE posts ADD COLUMN accounts TEXT[] NOT NULL DEFAULT '{}';
