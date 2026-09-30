-- name: LastGoBackups :many
-- Each project's last good backup.
SELECT * FROM backup_runs WHERE id IN (SELECT MAX(id) FROM backup_runs WHERE operation = 'backup' AND status = 'go' GROUP BY project_id);

-- name: FailedBackupProjects :many
-- The projects whose latest backup is NO-GO.
SELECT project_id FROM backup_runs WHERE id IN (SELECT MAX(id) FROM backup_runs WHERE operation = 'backup' GROUP BY project_id) AND status = 'no_go';

-- name: DeletingProjects :many
SELECT project_id FROM project_deletions WHERE project_id IS NOT NULL
  AND (status IN ('queued', 'running') OR (status = 'no_go' AND removing_at IS NOT NULL));

-- name: ShownUpdate :one
-- What the board says of the server's update: the running one, or the last
-- to finish since @since.
SELECT * FROM server_updates WHERE status = 'running' OR finished_at >= @since
ORDER BY status = 'running' DESC, finished_at DESC LIMIT 1;

-- name: DefaultStorage :one
SELECT * FROM storage_locations WHERE is_default LIMIT 1;
