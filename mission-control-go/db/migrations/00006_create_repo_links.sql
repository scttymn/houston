-- Add project's drafts: a repo URL and the deploy key made for it, then
-- what was read from its compose.yml. Drafts last a day. The deploy key
-- and webhook secret are crypt.String (sqlc.yaml).
-- +goose Up
CREATE TABLE repo_links (
  id INTEGER PRIMARY KEY,
  repo_url TEXT NOT NULL,
  branch TEXT NOT NULL DEFAULT 'main',
  compose_path TEXT NOT NULL DEFAULT 'compose.yml',
  deploy_key_private TEXT NOT NULL,
  deploy_key_public TEXT NOT NULL,
  webhook_secret TEXT NOT NULL DEFAULT '',
  -- houston inspect's answer, JSON; NULL until the file is read.
  preview TEXT,
  preview_sha TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE repo_links;
