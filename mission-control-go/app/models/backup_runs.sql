-- name: BackupLocationFor :one
-- Where a project's backups go: its own location once set up, else the
-- default (once setup's storage step is finished).
SELECT location.* FROM storage_locations location
WHERE location.acknowledged_at IS NOT NULL AND (location.id = @chosen OR (location.is_default AND NOT EXISTS (
  SELECT 1 FROM storage_locations chosen WHERE chosen.id = @chosen AND chosen.acknowledged_at IS NOT NULL)))
ORDER BY location.id LIMIT 1; -- the chosen one, or else the defaults: never both

-- name: DeploySnapshot :one
SELECT * FROM backup_runs WHERE project_id = ? AND operation = 'backup' AND reason = ? AND deploy_number = ?;

-- name: RestoreRun :one
SELECT * FROM backup_runs WHERE project_id = ? AND operation = 'restore' AND deploy_number = ?;

-- name: CreateBackupRun :one
INSERT INTO backup_runs (project_id, location_id, operation, kind, reason, deploy_number, source_snapshot_id, heartbeat_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: LastBackup :one
-- The project's last snapshot run: what "last backup" means (a restore's
-- data is put back from one and has none of its own).
SELECT * FROM backup_runs WHERE project_id = ? AND operation = 'backup' ORDER BY id DESC LIMIT 1;

-- name: ProjectBackupRun :one
SELECT * FROM backup_runs WHERE project_id = ? AND id = ?;

-- name: LatestBackupRun :one
SELECT * FROM backup_runs WHERE project_id = ? ORDER BY id DESC LIMIT 1;

-- name: VerifiedLocations :many
SELECT * FROM storage_locations WHERE verified_at IS NOT NULL ORDER BY name;

-- name: ProjectsUsingLocation :many
-- The projects backing up to a location: their own choice of it, or the
-- default for those without one.
SELECT id FROM projects WHERE backup_location_id = @location
  OR (backup_location_id IS NULL AND @is_default_and_ready);

-- name: LocationLastWrite :one
SELECT finished_at FROM backup_runs WHERE location_id = ? AND status = 'go' AND finished_at IS NOT NULL ORDER BY finished_at DESC LIMIT 1;
