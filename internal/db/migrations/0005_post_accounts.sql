-- Accounts a post goes to, for publishers that target individual accounts (e.g. the MCP publisher).
ALTER TABLE posts ADD COLUMN accounts TEXT[] NOT NULL DEFAULT '{}';
