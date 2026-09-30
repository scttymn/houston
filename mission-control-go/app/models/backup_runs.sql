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

-- name: QueuedManualBackup :one
SELECT * FROM backup_runs WHERE project_id = ? AND status = 'queued' AND reason = 'manual';

-- name: BackupRunByID :one
SELECT * FROM backup_runs WHERE id = ?;

-- name: StaleRunning :many
SELECT * FROM backup_runs WHERE project_id = ? AND status = 'running' AND heartbeat_at < ?;

-- name: AbandonRun :exec
UPDATE backup_runs SET status = 'no_go', finished_at = @now, error = @error, updated_at = @now WHERE id = @id;

-- name: OtherRunning :one
SELECT EXISTS (SELECT 1 FROM backup_runs WHERE project_id = ? AND status = 'running' AND id != ?) AS running;

-- name: RestoreUnderway :one
SELECT EXISTS (SELECT 1 FROM deploys WHERE project_id = ? AND kind = 'restore' AND status IN ('queued', 'in_flight')) AS underway;

-- name: ClaimRun :execrows
UPDATE backup_runs SET status = 'running', token_digest = @token_digest, heartbeat_at = @now, started_at = @now, updated_at = @now
WHERE id = @id AND status = 'queued';

-- Writes below go through the token: only the job holding it may write,
-- and only while the run is running (one abandoned and taken over stays).

-- name: BeatRun :execrows
UPDATE backup_runs SET heartbeat_at = @now WHERE id = @id AND status = 'running' AND token_digest = @token_digest;

-- name: BeginRun :execrows
UPDATE backup_runs SET sha = @sha, updated_at = @now WHERE id = @id AND status = 'running' AND token_digest = @token_digest;

-- name: FinishRun :execrows
UPDATE backup_runs SET status = @status, error = @error, snapshot_id = @snapshot_id, bytes = @bytes, found = @found, log = @log,
  finished_at = @now, updated_at = @now
WHERE id = @id AND status = 'running' AND token_digest = @token_digest;

-- name: GiveUpRun :execrows
-- A queued run that will never start (its job gave up waiting).
UPDATE backup_runs SET status = 'no_go', error = @error, finished_at = @now, updated_at = @now WHERE id = @id AND status = 'queued';

-- name: LocationsUsedBy :many
-- The set-up locations a project's backups went to.
SELECT * FROM storage_locations WHERE acknowledged_at IS NOT NULL
  AND id IN (SELECT location_id FROM backup_runs WHERE project_id = ? AND operation = 'backup') ORDER BY name;

-- name: ScheduledRun :one
SELECT * FROM backup_runs WHERE project_id = ? AND scheduled_for = ?;

-- name: CreateScheduledRun :one
INSERT INTO backup_runs (project_id, location_id, operation, kind, reason, scheduled_for, heartbeat_at)
VALUES (?, ?, 'backup', 'auto', 'schedule', ?, ?) RETURNING *;

-- name: PrunedLocations :many
-- The set-up locations any backup went to: what prune cleans.
SELECT * FROM storage_locations WHERE acknowledged_at IS NOT NULL AND id IN (SELECT DISTINCT location_id FROM backup_runs) ORDER BY id;

-- name: SetPruned :exec
UPDATE storage_locations SET pruned_at = ?, prune_error = '', updated_at = ? WHERE id = ?;

-- name: SetPruneError :exec
UPDATE storage_locations SET prune_error = ?, updated_at = ? WHERE id = ?;
