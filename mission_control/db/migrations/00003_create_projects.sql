-- The projects Houston deploys, as last synced from their compose.yml, and
-- what hangs off each: the container names it owns, its secrets' values,
-- its deploys, and the runners that claim them. Lists and maps the sync
-- sends are JSON (db.JSON); secrets are crypt.String (sqlc.yaml).
-- +goose Up
-- Where backups and volumes live: a folder, an NFS export, S3 or B2.
-- Credentials and the restic password are crypt.String (credentials JSON
-- inside).
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

-- Each container-name prefix a project owns: the unique name keeps two
-- projects from sharing one.
CREATE TABLE project_hosts (
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  name TEXT NOT NULL UNIQUE,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX project_hosts_project_id ON project_hosts (project_id);

CREATE TABLE secrets (
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  value TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (project_id, key)
);

-- One run of houston deploy (or a runner's, a restore, a copy's first
-- deploy), numbered per project. The partial unique indexes are the locks:
-- one in flight and one queued a project.
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
CREATE UNIQUE INDEX deploys_one_in_flight ON deploys (project_id) WHERE status = 'in_flight';
CREATE UNIQUE INDEX deploys_one_queued ON deploys (project_id) WHERE status = 'queued';

-- Where each of a project's named volumes lives: NULL is local disk.
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

CREATE TABLE runners (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  last_seen_at DATETIME NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE runners;
DROP TABLE project_volumes;
DROP TABLE deploys;
DROP TABLE secrets;
DROP TABLE project_hosts;
DROP TABLE projects;
DROP TABLE storage_locations;
