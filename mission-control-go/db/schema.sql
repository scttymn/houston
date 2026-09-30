-- The schema as the migrations leave it, for sqlc and for reading. It's
-- written from the migrations; don't edit it by hand.

CREATE TABLE api_tokens (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 50),
  token_digest TEXT NOT NULL UNIQUE,
  last_used_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
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
CREATE TABLE deploys (
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  number INTEGER NOT NULL,
  kind TEXT NOT NULL DEFAULT 'deploy' CHECK (kind IN ('deploy', 'restore', 'copy')),
  status TEXT NOT NULL DEFAULT 'in_flight' CHECK (status IN ('queued', 'in_flight', 'go', 'no_go', 'hold')),
  sha TEXT NOT NULL,
  ref TEXT NOT NULL,
  fresh BOOLEAN NOT NULL DEFAULT FALSE,
  generation INTEGER NOT NULL DEFAULT 1,
  step TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  log TEXT NOT NULL DEFAULT '',
  -- The runner that claimed it; '' for a houston deploy by hand.
  runner TEXT NOT NULL DEFAULT '',
  proposed_name TEXT NOT NULL DEFAULT '',
  token_digest TEXT NOT NULL DEFAULT '',
  heartbeat_at DATETIME NOT NULL,
  finished_at DATETIME,
  switched_at DATETIME,
  source_location_id INTEGER REFERENCES storage_locations (id),
  source_snapshot_id TEXT NOT NULL DEFAULT '',
  -- A restore's check sync, applied at its switch; NULL without one.
  sync_payload TEXT,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (project_id, number)
);
CREATE TABLE installations (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  base_domain TEXT NOT NULL DEFAULT '',
  time_zone TEXT NOT NULL DEFAULT 'UTC',
  dns_mode TEXT NOT NULL DEFAULT '',
  port_open BOOLEAN NOT NULL DEFAULT TRUE,
  cloudflare_account_id TEXT NOT NULL DEFAULT '',
  cloudflare_zone_id TEXT NOT NULL DEFAULT '',
  cloudflare_api_token TEXT NOT NULL DEFAULT '',
  cloudflare_connected_at DATETIME,
  tunnel_id TEXT NOT NULL DEFAULT '',
  tunnel_token TEXT NOT NULL DEFAULT '',
  latest_release TEXT NOT NULL DEFAULT '',
  latest_release_url TEXT NOT NULL DEFAULT '',
  latest_release_checked_at DATETIME,
  registry_cleanup_since DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
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
CREATE TABLE project_hosts (
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  name TEXT NOT NULL UNIQUE,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE project_volumes (
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES projects (id),
  name TEXT NOT NULL,
  location_id INTEGER REFERENCES storage_locations (id),
  placed_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (project_id, name)
);
CREATE TABLE projects (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  app_service TEXT NOT NULL,
  services TEXT NOT NULL DEFAULT '[]',
  domains TEXT NOT NULL DEFAULT '[]',
  -- domain -> {state, reason}, as the last sync or GO pointed them
  domain_states TEXT NOT NULL DEFAULT '{}',
  variables TEXT NOT NULL DEFAULT '[]',
  volumes TEXT NOT NULL DEFAULT '[]',
  databases TEXT NOT NULL DEFAULT '[]',
  details TEXT NOT NULL DEFAULT '{}',
  deploy_rule TEXT NOT NULL DEFAULT '{}',
  health TEXT NOT NULL,
  port INTEGER NOT NULL,
  -- The data generation serving: a restore builds the next beside it.
  data_generation INTEGER NOT NULL DEFAULT 1,
  keep_auto INTEGER NOT NULL DEFAULT 14,
  keep_deploy INTEGER NOT NULL DEFAULT 10,
  backup_schedule TEXT NOT NULL DEFAULT 'daily 03:00',
  backup_location_id INTEGER REFERENCES storage_locations (id),
  repo_url TEXT NOT NULL DEFAULT '',
  branch TEXT NOT NULL DEFAULT '',
  compose_path TEXT NOT NULL DEFAULT '',
  deploy_key_private TEXT NOT NULL DEFAULT '',
  deploy_key_public TEXT NOT NULL DEFAULT '',
  webhook_secret TEXT NOT NULL DEFAULT '',
  webhook_verified_at DATETIME,
  seen_refs TEXT NOT NULL DEFAULT '{}',
  last_checked_at DATETIME,
  last_check_error TEXT NOT NULL DEFAULT '',
  maintenance_since DATETIME,
  maintenance_by TEXT NOT NULL DEFAULT '',
  maintenance_message TEXT NOT NULL DEFAULT '' CHECK (length(maintenance_message) <= 500),
  -- The project's own maintenance page; '' is Houston's.
  maintenance_page TEXT NOT NULL DEFAULT '',
  synced_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
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
CREATE TABLE runners (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  last_seen_at DATETIME NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE secrets (
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  value TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (project_id, key)
);
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
CREATE TABLE storage_locations (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL CHECK (kind IN ('nfs', 'local', 's3', 'b2')),
  settings TEXT NOT NULL DEFAULT '{}',
  credentials TEXT NOT NULL DEFAULT '',
  restic_password TEXT NOT NULL DEFAULT '',
  is_default BOOLEAN NOT NULL DEFAULT FALSE,
  acknowledged_at DATETIME,
  verified_at DATETIME,
  pruned_at DATETIME,
  prune_error TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX backup_runs_location ON backup_runs (location_id);
CREATE UNIQUE INDEX backup_runs_one_per_deploy ON backup_runs (project_id, deploy_number) WHERE reason = 'deploy';
CREATE UNIQUE INDEX backup_runs_one_queued_manual ON backup_runs (project_id) WHERE status = 'queued' AND reason = 'manual';
CREATE UNIQUE INDEX backup_runs_one_restore_per_deploy ON backup_runs (project_id, deploy_number) WHERE operation = 'restore';
CREATE UNIQUE INDEX backup_runs_one_running ON backup_runs (project_id) WHERE status = 'running';
CREATE UNIQUE INDEX backup_runs_one_safety_snapshot_per_restore ON backup_runs (project_id, deploy_number) WHERE operation = 'backup' AND reason = 'restore';
CREATE UNIQUE INDEX backup_runs_one_scheduled_per_day ON backup_runs (project_id, scheduled_for) WHERE scheduled_for IS NOT NULL;
CREATE INDEX backup_runs_project_created ON backup_runs (project_id, created_at);
CREATE UNIQUE INDEX deploys_one_in_flight ON deploys (project_id) WHERE status = 'in_flight';
CREATE UNIQUE INDEX deploys_one_queued ON deploys (project_id) WHERE status = 'queued';
CREATE INDEX project_copies_from_name ON project_copies (from_name);
CREATE UNIQUE INDEX project_copies_one_active ON project_copies (from_project_id) WHERE status IN ('queued', 'running');
CREATE INDEX project_copies_project ON project_copies (project_id);
CREATE INDEX project_deletions_name ON project_deletions (name);
CREATE UNIQUE INDEX project_deletions_one_active ON project_deletions (project_id) WHERE status IN ('queued', 'running');
CREATE INDEX project_deletions_project ON project_deletions (project_id);
CREATE INDEX project_hosts_project_id ON project_hosts (project_id);
CREATE UNIQUE INDEX server_updates_one_running ON server_updates (status) WHERE status = 'running';
