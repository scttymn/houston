-- A project's backups and restores (restic, run by the backup job): a
-- snapshot (operation backup) or a restore's data put back from one. The
-- partial unique indexes are the locks.
-- +goose Up
CREATE TABLE backup_runs (
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES projects (id),
  location_id INTEGER NOT NULL REFERENCES storage_locations (id),
  operation TEXT NOT NULL DEFAULT 'backup' CHECK (operation IN ('backup', 'restore')),
  -- final: a deleted project's last snapshot, which retention never forgets.
  kind TEXT NOT NULL CHECK (kind IN ('auto', 'deploy', 'restore', 'final')),
  reason TEXT NOT NULL CHECK (reason IN ('schedule', 'manual', 'deploy', 'restore', 'delete', 'copy')),
  status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'go', 'no_go', 'skipped')),
  deploy_number INTEGER,
  -- The local day a scheduled backup is for (YYYY-MM-DD).
  scheduled_for TEXT,
  sha TEXT NOT NULL DEFAULT '',
  snapshot_id TEXT NOT NULL DEFAULT '',
  source_snapshot_id TEXT NOT NULL DEFAULT '',
  bytes INTEGER,
  -- What the backup found to hold (volumes, databases).
  found TEXT NOT NULL DEFAULT '{}',
  error TEXT NOT NULL DEFAULT '' CHECK (length(error) <= 4000),
  log TEXT NOT NULL DEFAULT '',
  token_digest TEXT NOT NULL DEFAULT '',
  heartbeat_at DATETIME NOT NULL,
  started_at DATETIME,
  finished_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX backup_runs_project_created ON backup_runs (project_id, created_at);
CREATE INDEX backup_runs_location ON backup_runs (location_id);
CREATE UNIQUE INDEX backup_runs_one_per_deploy ON backup_runs (project_id, deploy_number) WHERE reason = 'deploy';
CREATE UNIQUE INDEX backup_runs_one_restore_per_deploy ON backup_runs (project_id, deploy_number) WHERE operation = 'restore';
CREATE UNIQUE INDEX backup_runs_one_safety_snapshot_per_restore ON backup_runs (project_id, deploy_number) WHERE operation = 'backup' AND reason = 'restore';
CREATE UNIQUE INDEX backup_runs_one_scheduled_per_day ON backup_runs (project_id, scheduled_for) WHERE scheduled_for IS NOT NULL;
CREATE UNIQUE INDEX backup_runs_one_queued_manual ON backup_runs (project_id) WHERE status = 'queued' AND reason = 'manual';
CREATE UNIQUE INDEX backup_runs_one_running ON backup_runs (project_id) WHERE status = 'running';

-- +goose Down
DROP TABLE backup_runs;
