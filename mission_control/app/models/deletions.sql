-- A deletion holds its project while it's queued or running, and after a
-- removal step stopped it (asking again resumes it).

-- name: HoldingDeletion :one
SELECT * FROM project_deletions WHERE project_id = ?
  AND (status IN ('queued', 'running') OR (status = 'no_go' AND removing_at IS NOT NULL)) ORDER BY id DESC LIMIT 1;

-- name: DeletionByID :one
SELECT * FROM project_deletions WHERE id = ?;

-- name: CreateDeletion :one
INSERT INTO project_deletions (project_id, name, repo_url, requested_by, delete_backups, heartbeat_at)
VALUES (?, ?, ?, ?, ?, ?) RETURNING *;

-- name: RequeueDeletion :one
UPDATE project_deletions SET status = 'queued', error = '', finished_at = NULL, heartbeat_at = @now, updated_at = @now WHERE id = @id RETURNING *;

-- name: BusyBackupRun :one
SELECT * FROM backup_runs WHERE project_id = ? AND status IN ('queued', 'running') ORDER BY id LIMIT 1;

-- name: DeletionSnapshotLocations :many
-- The locations holding the final snapshots of deleted projects of a name.
SELECT location.* FROM storage_locations location
WHERE location.acknowledged_at IS NOT NULL
  AND location.id IN (SELECT deletion.snapshot_location_id FROM project_deletions deletion WHERE deletion.name = ?) ORDER BY location.name;

-- name: ClaimDeletion :execrows
UPDATE project_deletions SET status = 'running', started_at = @now, heartbeat_at = @now, updated_at = @now WHERE id = @id AND status = 'queued';

-- Writes below go through the claim: only while the deletion is still this
-- job's (asking again takes a silent one over).

-- name: WriteDeletion :execrows
UPDATE project_deletions SET log = @log, step = @step, updated_at = @now
WHERE id = @id AND status = 'running' AND started_at = @started_at;

-- name: BeatDeletion :exec
UPDATE project_deletions SET heartbeat_at = @now WHERE id = @id AND status = 'running' AND started_at = @started_at;

-- name: MarkRemoving :exec
UPDATE project_deletions SET removing_at = @now, updated_at = @now WHERE id = @id AND status = 'running' AND started_at = @started_at;

-- name: KeepFinalSnapshot :exec
UPDATE project_deletions SET snapshot_id = @snapshot_id, snapshot_location_id = @location_id, updated_at = @now
WHERE id = @id AND status = 'running' AND started_at = @started_at;

-- name: FinishDeletion :exec
UPDATE project_deletions SET status = @status, error = @error, log = @log, finished_at = @now, updated_at = @now
WHERE id = @id AND status = 'running' AND started_at = @started_at;

-- name: NoteDeletion :exec
-- A line on the log after it's done (the registry's clean-up).
UPDATE project_deletions SET log = log || @line, updated_at = @now WHERE id = @id;

-- name: VolumeLocationsOf :many
SELECT * FROM storage_locations WHERE kind IN ('nfs', 'local')
  AND id IN (SELECT location_id FROM project_volumes WHERE project_id = ? AND location_id IS NOT NULL) ORDER BY name;

-- name: OthersHostNames :many
SELECT name FROM project_hosts WHERE project_id != ?;

-- name: DeleteProjectRows :exec
DELETE FROM backup_runs WHERE project_id = ?;

-- name: DeleteProjectVolumes :exec
DELETE FROM project_volumes WHERE project_id = ?;

-- name: DeleteProject :exec
-- Its deploys, secrets and container names go with it; its deletions stay.
DELETE FROM projects WHERE id = ?;

-- name: LiveDeployInFlight :one
SELECT deploys.*, projects.name AS project FROM deploys JOIN projects ON projects.id = deploys.project_id
WHERE deploys.status = 'in_flight' AND deploys.heartbeat_at >= ? ORDER BY deploys.id LIMIT 1;

-- name: TakeRegistryLock :execrows
UPDATE installations SET registry_cleanup_since = @now WHERE registry_cleanup_since IS NULL OR registry_cleanup_since < @stale;

-- name: ReleaseRegistryLock :exec
UPDATE installations SET registry_cleanup_since = NULL;
