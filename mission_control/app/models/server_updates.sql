-- name: RunningUpdate :one
SELECT * FROM server_updates WHERE status = 'running' LIMIT 1;

-- name: UpdateRunning :one
SELECT EXISTS (SELECT 1 FROM server_updates WHERE status = 'running') AS running;

-- name: LastUpdate :one
SELECT * FROM server_updates ORDER BY id DESC LIMIT 1;

-- name: StartUpdate :one
INSERT INTO server_updates (to_version, from_version, started_at) VALUES (?, ?, ?) RETURNING *;

-- name: DeleteUpdate :exec
DELETE FROM server_updates WHERE id = ?;

-- name: FollowUpdate :exec
UPDATE server_updates SET step = ?, log = ?, updated_at = ? WHERE id = ?;

-- name: SettleUpdate :execrows
-- Once: only while it's running.
UPDATE server_updates SET status = @status, log = @log, finished_at = @now, updated_at = @now WHERE id = @id AND status = 'running';

-- name: FirstBusyDeploy :one
-- The oldest deploy an update would cut short.
SELECT d.number, d.kind, d.status, p.name AS project FROM deploys d JOIN projects p ON p.id = d.project_id
WHERE d.status IN ('queued', 'in_flight') ORDER BY d.id LIMIT 1;

-- name: FirstBusyRun :one
-- The oldest backup run an update would cut short.
SELECT r.operation, r.status, p.name AS project FROM backup_runs r JOIN projects p ON p.id = r.project_id
WHERE r.status IN ('queued', 'running') ORDER BY r.id LIMIT 1;
