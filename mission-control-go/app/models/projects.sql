-- name: ProjectByName :one
SELECT * FROM projects WHERE name = ?;

-- name: ProjectByID :one
SELECT * FROM projects WHERE id = ?;

-- name: SaveSynced :one
-- A sync's project: made, or updated with what compose.yml says now.
INSERT INTO projects (name, app_service, services, domains, variables, health, port, deploy_rule, volumes, databases,
  keep_auto, keep_deploy, backup_schedule, maintenance_page, details, synced_at, updated_at)
VALUES (@name, @app_service, @services, @domains, @variables, @health, @port, @deploy_rule, @volumes, @databases,
  @keep_auto, @keep_deploy, @backup_schedule, @maintenance_page, @details, @now, @now)
ON CONFLICT (name) DO UPDATE SET app_service = excluded.app_service, services = excluded.services,
  domains = excluded.domains, variables = excluded.variables, health = excluded.health, port = excluded.port,
  deploy_rule = excluded.deploy_rule, volumes = excluded.volumes, databases = excluded.databases,
  keep_auto = excluded.keep_auto, keep_deploy = excluded.keep_deploy, backup_schedule = excluded.backup_schedule,
  maintenance_page = excluded.maintenance_page, details = excluded.details, synced_at = excluded.synced_at,
  updated_at = excluded.updated_at
RETURNING *;

-- name: SetDomainStates :exec
UPDATE projects SET domain_states = ?, updated_at = ? WHERE id = ?;

-- name: MoveGenerationForward :execrows
-- Only ever forward, in one statement: no stale read of the project.
UPDATE projects SET data_generation = @generation, updated_at = @now WHERE id = @id AND data_generation < @generation;

-- name: ProjectHostNames :many
SELECT name FROM project_hosts WHERE project_id = ? ORDER BY name;

-- name: ReleaseProjectHosts :exec
-- The names it no longer has.
DELETE FROM project_hosts WHERE project_id = @project_id AND name NOT IN (sqlc.slice('keep'));

-- name: ClaimProjectHost :exec
-- The unique name decides: another project's is refused.
INSERT INTO project_hosts (project_id, name) VALUES (?, ?);

-- name: ProjectHostOwner :one
-- Which other project owns one of these names. (@project before the slice:
-- sqlc numbers a named parameter after one, and the slice's expansion
-- shifts what the number means.)
SELECT project_hosts.name, projects.name AS project FROM project_hosts JOIN projects ON projects.id = project_hosts.project_id
WHERE projects.name != @project AND project_hosts.name IN (sqlc.slice('names')) ORDER BY project_hosts.name LIMIT 1;

-- name: ProjectSecrets :many
SELECT key, value FROM secrets WHERE project_id = ? ORDER BY key;

-- name: CreateSecret :exec
INSERT INTO secrets (project_id, key, value) VALUES (?, ?, ?);

-- name: ProjectsInMaintenance :many
SELECT * FROM projects WHERE maintenance_since IS NOT NULL ORDER BY name;

-- name: MarkWebhookVerified :exec
UPDATE projects SET webhook_verified_at = ? WHERE id = ? AND webhook_verified_at IS NULL;

-- name: Projects :many
SELECT * FROM projects ORDER BY name;

-- name: ProjectExists :one
SELECT EXISTS (SELECT 1 FROM projects WHERE name = ?) AS found;

-- name: ProjectSecretRows :many
SELECT key, value, updated_at FROM secrets WHERE project_id = ?;

-- name: ProjectVolumeRows :many
SELECT project_volumes.name, project_volumes.placed_at, storage_locations.name AS location
FROM project_volumes LEFT JOIN storage_locations ON storage_locations.id = project_volumes.location_id
WHERE project_volumes.project_id = ?;

-- name: SetTimeZone :exec
UPDATE installations SET time_zone = ?, updated_at = ? WHERE id = 1;

-- name: SaveSecret :exec
INSERT INTO secrets (project_id, key, value, updated_at) VALUES (@project_id, @key, @value, @now)
ON CONFLICT (project_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;

-- name: DeleteSecret :exec
DELETE FROM secrets WHERE project_id = ? AND key = ?;

-- name: RotateWebhookSecret :one
UPDATE projects SET webhook_secret = ?, webhook_verified_at = NULL, updated_at = ? WHERE id = ? RETURNING *;

-- name: SetMaintenance :exec
UPDATE projects SET maintenance_since = ?, maintenance_by = ?, maintenance_message = ?, updated_at = ? WHERE id = ?;

-- name: SetBackupLocation :exec
UPDATE projects SET backup_location_id = ?, updated_at = ? WHERE id = ?;

-- name: SetVolumeLocation :exec
UPDATE project_volumes SET location_id = ?, updated_at = ? WHERE id = ?;

-- name: StorageLocationByName :one
SELECT * FROM storage_locations WHERE name = ?;

-- name: MakeDefaultLocation :exec
UPDATE storage_locations SET is_default = (id = @id), updated_at = @now;
