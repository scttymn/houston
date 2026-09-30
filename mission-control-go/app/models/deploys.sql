-- name: DeployByID :one
SELECT * FROM deploys WHERE id = ?;

-- name: RestoreHolding :one
-- A restore that owns its project's config: queued, or in flight and heard
-- from since @since.
SELECT * FROM deploys WHERE project_id = @project_id AND kind = 'restore'
  AND (status = 'queued' OR (status = 'in_flight' AND heartbeat_at >= @since)) ORDER BY id LIMIT 1;

-- name: KeepRestoreSync :execrows
-- A restore's check sync, kept while it's in flight, applied at its switch.
UPDATE deploys SET sync_payload = ? WHERE id = ? AND status = 'in_flight';

-- name: RestoreServing :one
-- The restore that built what kamal-proxy serves: of two of one commit, the
-- one whose compose.yml was kept, then the newer.
SELECT * FROM deploys WHERE project_id = ? AND kind = 'restore' AND generation = ? AND sha = ?
ORDER BY sync_payload IS NULL, number DESC LIMIT 1;

-- name: MarkSwitched :exec
UPDATE deploys SET switched_at = ? WHERE id = ? AND switched_at IS NULL;

-- name: ProjectHasServed :one
-- Whether anything it deployed or restored has served: a GO, or a switch.
SELECT EXISTS (SELECT 1 FROM deploys WHERE project_id = ? AND (status = 'go' OR switched_at IS NOT NULL)) AS served;

-- name: CreateDeploy :one
INSERT INTO deploys (project_id, number, kind, status, sha, ref, generation, token_digest, heartbeat_at, sync_payload)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;
