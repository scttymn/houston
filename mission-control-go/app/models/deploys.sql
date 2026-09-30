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

-- name: DeployInFlight :one
SELECT * FROM deploys WHERE project_id = ? AND status = 'in_flight';

-- name: AbandonDeploy :exec
UPDATE deploys SET status = 'no_go', finished_at = @now, error = @error, updated_at = @now WHERE id = @id;

-- name: NextDeployNumber :one
SELECT CAST(COALESCE(MAX(number), 0) + 1 AS INTEGER) AS next FROM deploys WHERE project_id = ?;

-- name: SaveProgress :one
UPDATE deploys SET step = ?, error = ?, proposed_name = ?, status = ?, finished_at = ?, heartbeat_at = ?, updated_at = ?
WHERE id = ? RETURNING *;

-- name: LogSize :one
-- In bytes, without reading a log that can be megabytes.
SELECT CAST(length(CAST(log AS BLOB)) AS INTEGER) AS log_size FROM deploys WHERE id = ?;

-- name: AppendLog :exec
UPDATE deploys SET log = log || ? WHERE id = ?;

-- name: FirstGo :one
-- Whether nothing else of the project's has served: no other GO, no other
-- switched restore.
SELECT NOT EXISTS (SELECT 1 FROM deploys WHERE project_id = ? AND id != ? AND (status = 'go' OR switched_at IS NOT NULL)) AS is_first;

-- name: StaleInFlight :many
SELECT * FROM deploys WHERE status = 'in_flight' AND heartbeat_at < ? ORDER BY id;

-- name: NextQueued :one
-- The oldest queued deploy whose project has none in flight.
SELECT * FROM deploys WHERE status = 'queued'
  AND project_id NOT IN (SELECT project_id FROM deploys WHERE status = 'in_flight')
ORDER BY created_at, id LIMIT 1;

-- name: ClaimQueued :execrows
-- Only if it's still queued: another runner may have had it since.
UPDATE deploys SET status = 'in_flight', runner = @runner, token_digest = @token_digest, generation = @generation,
  heartbeat_at = @now, updated_at = @now
WHERE id = @id AND status = 'queued';

-- name: SeeRunner :exec
INSERT INTO runners (name, last_seen_at, updated_at) VALUES (@name, @now, @now)
ON CONFLICT (name) DO UPDATE SET last_seen_at = excluded.last_seen_at, updated_at = excluded.updated_at;

-- A deploy without its log (up to 4 MiB): for lists. The columns are the
-- table's, in order, so each row converts to a Deploy.

-- name: DeploySummaries :many
SELECT id, project_id, number, kind, status, sha, ref, fresh, generation, step, error, CAST('' AS TEXT) AS log, runner, proposed_name,
  token_digest, heartbeat_at, finished_at, switched_at, source_location_id, source_snapshot_id, sync_payload, created_at, updated_at
FROM deploys WHERE project_id = ? ORDER BY number DESC LIMIT ? OFFSET ?;

-- name: RunningDeploySummary :one
-- The latest GO, or a restore that switched without getting to GO,
-- whichever is newer.
SELECT id, project_id, number, kind, status, sha, ref, fresh, generation, step, error, CAST('' AS TEXT) AS log, runner, proposed_name,
  token_digest, heartbeat_at, finished_at, switched_at, source_location_id, source_snapshot_id, sync_payload, created_at, updated_at
FROM deploys WHERE project_id = ? AND (status = 'go' OR switched_at IS NOT NULL) ORDER BY number DESC LIMIT 1;

-- name: DeployByNumber :one
SELECT * FROM deploys WHERE project_id = ? AND number = ?;

-- name: LatestDeployByNumber :one
SELECT * FROM deploys WHERE project_id = ? ORDER BY number DESC LIMIT 1;

-- name: QueuedDeploy :one
SELECT * FROM deploys WHERE project_id = ? AND status = 'queued';

-- name: RequeueDeploy :one
UPDATE deploys SET sha = ?, ref = ?, fresh = ?, log = ?, updated_at = ? WHERE id = ? RETURNING *;

-- name: RestoreOrCopyUnderway :one
-- A restore or a copy queued or in flight owns what's deployed next.
SELECT EXISTS (SELECT 1 FROM deploys WHERE project_id = ? AND kind IN ('restore', 'copy') AND status IN ('queued', 'in_flight')) AS underway;

-- name: BusyDeploy :one
SELECT * FROM deploys WHERE project_id = ? AND status IN ('queued', 'in_flight') ORDER BY number LIMIT 1;

-- name: CreateRestore :one
INSERT INTO deploys (project_id, number, kind, status, sha, ref, generation, heartbeat_at, source_snapshot_id, source_location_id)
VALUES (?, ?, 'restore', 'queued', ?, ?, ?, ?, ?, ?) RETURNING *;
