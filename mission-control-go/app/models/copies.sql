-- name: ActiveCopyFrom :one
SELECT * FROM project_copies WHERE from_project_id = ? AND status IN ('queued', 'running') ORDER BY id LIMIT 1;

-- name: CopyOf :one
-- The copy that made a project: its latest.
SELECT * FROM project_copies WHERE project_id = ? ORDER BY id DESC LIMIT 1;

-- name: CopyByDeploy :one
SELECT * FROM project_copies WHERE deploy_id = ? ORDER BY id DESC LIMIT 1;

-- name: CreateCopy :one
INSERT INTO project_copies (project_id, from_project_id, deploy_id, from_name, to_name, sha, requested_by)
VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: SettleCopy :exec
UPDATE project_copies SET status = @status, error = @error, log = @log, updated_at = @now WHERE id = @id;

-- name: SetCopySnapshot :exec
UPDATE project_copies SET snapshot_run_id = ?, updated_at = ? WHERE id = ?;

-- name: SetHandedOver :exec
UPDATE project_copies SET handed_over = ?, handed_over_at = ?, updated_at = ? WHERE id = ?;

-- name: MarkUndone :exec
UPDATE project_copies SET undone_at = @now, updated_at = @now WHERE id = @id;

-- name: CopyWebhookTarget :one
-- A deleted project's webhook rings its copy: the newest one that went.
SELECT project_id FROM project_copies WHERE from_name = ? AND status = 'go' AND from_project_id IS NULL AND project_id IS NOT NULL
ORDER BY id DESC LIMIT 1;

-- name: CopyProjectSettings :exec
UPDATE projects SET webhook_verified_at = ?, seen_refs = ?, backup_location_id = ?, updated_at = ? WHERE id = ?;

-- name: CreateCopyDeploy :one
INSERT INTO deploys (project_id, number, kind, status, sha, ref, heartbeat_at, sync_payload)
VALUES (?, 1, 'copy', 'queued', ?, ?, ?, ?) RETURNING *;

-- name: CancelDeploy :exec
UPDATE deploys SET status = 'no_go', error = @error, finished_at = @now, updated_at = @now WHERE id = @id;

-- name: CreateVolumeChoice :exec
INSERT INTO project_volumes (project_id, name, location_id) VALUES (?, ?, ?);

-- name: ProjectVolumeChoices :many
SELECT * FROM project_volumes WHERE project_id = ?;

-- name: DeployLog :one
SELECT log FROM deploys WHERE id = ?;

-- name: CopySnapshotRun :one
SELECT * FROM backup_runs WHERE project_id = ? AND reason = 'copy' AND id = ?;

-- name: CreateCopySnapshot :one
INSERT INTO backup_runs (project_id, location_id, operation, kind, reason, heartbeat_at) VALUES (?, ?, 'backup', 'deploy', 'copy', ?) RETURNING *;

-- name: SetDeploySource :exec
UPDATE deploys SET source_snapshot_id = ?, source_location_id = ?, updated_at = ? WHERE id = ? AND source_snapshot_id = '';

-- name: CopyByID :one
SELECT * FROM project_copies WHERE id = ?;
