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
