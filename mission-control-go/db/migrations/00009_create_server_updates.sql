-- A server update started from Mission Control: the helper container that
-- runs the release's installer on the host, and how it went. The running
-- one is the lock: one at a time, and deploys and backups wait on it.
-- +goose Up
CREATE TABLE server_updates (
  id INTEGER PRIMARY KEY,
  to_version TEXT NOT NULL,
  from_version TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'go', 'rolled_back', 'no_go')),
  -- The installer's latest step, and the helper's log (its last lines).
  step TEXT NOT NULL DEFAULT '',
  log TEXT NOT NULL DEFAULT '',
  started_at DATETIME NOT NULL,
  finished_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX server_updates_one_running ON server_updates (status) WHERE status = 'running';

-- +goose Down
DROP TABLE server_updates;
