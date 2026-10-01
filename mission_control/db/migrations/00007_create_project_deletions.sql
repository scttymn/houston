-- A project's deletion: its final snapshot, then its removal, step by
-- step. It outlives the project (project_id goes NULL): its name, repo and
-- final snapshot stay. One active a project.
-- +goose Up
CREATE TABLE project_deletions (
  id INTEGER PRIMARY KEY,
  project_id INTEGER REFERENCES projects (id) ON DELETE SET NULL,
  name TEXT NOT NULL,
  repo_url TEXT NOT NULL DEFAULT '',
  requested_by TEXT NOT NULL,
  delete_backups BOOLEAN NOT NULL DEFAULT FALSE,
  status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'go', 'no_go')),
  step TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '' CHECK (length(error) <= 4000),
  log TEXT NOT NULL DEFAULT '',
  snapshot_id TEXT NOT NULL DEFAULT '',
  snapshot_location_id INTEGER REFERENCES storage_locations (id),
  heartbeat_at DATETIME NOT NULL,
  started_at DATETIME,
  -- Set once removal begins: from then on, asking again resumes it.
  removing_at DATETIME,
  finished_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX project_deletions_name ON project_deletions (name);
CREATE INDEX project_deletions_project ON project_deletions (project_id);
CREATE UNIQUE INDEX project_deletions_one_active ON project_deletions (project_id) WHERE status IN ('queued', 'running');

-- +goose Down
DROP TABLE project_deletions;
