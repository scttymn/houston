-- A project copied to a new name (compose.yml's name: changed): the new
-- project, its first deploy (kind copy), the old project's snapshot it
-- starts from, and the hosts handed over. One active a project copied.
-- +goose Up
CREATE TABLE project_copies (
  id INTEGER PRIMARY KEY,
  project_id INTEGER REFERENCES projects (id) ON DELETE SET NULL,
  from_project_id INTEGER REFERENCES projects (id) ON DELETE SET NULL,
  deploy_id INTEGER REFERENCES deploys (id) ON DELETE SET NULL,
  snapshot_run_id INTEGER REFERENCES backup_runs (id) ON DELETE SET NULL,
  from_name TEXT NOT NULL,
  to_name TEXT NOT NULL,
  sha TEXT NOT NULL,
  requested_by TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'go', 'no_go')),
  error TEXT NOT NULL DEFAULT '' CHECK (length(error) <= 4000),
  log TEXT NOT NULL DEFAULT '',
  -- The hosts the new project took over from the old, JSON.
  handed_over TEXT NOT NULL DEFAULT '[]',
  handed_over_at DATETIME,
  undone_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX project_copies_from_name ON project_copies (from_name);
CREATE INDEX project_copies_project ON project_copies (project_id);
CREATE UNIQUE INDEX project_copies_one_active ON project_copies (from_project_id) WHERE status IN ('queued', 'running');

-- +goose Down
DROP TABLE project_copies;
