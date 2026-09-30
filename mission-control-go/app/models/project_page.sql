-- name: DeployCount :one
SELECT count(*) FROM deploys WHERE project_id = ?;

-- name: RecentFailedCopyFrom :one
-- A project's copy that failed since @since, unless a later one went.
SELECT * FROM project_copies c WHERE c.from_project_id = @project_id AND c.status = 'no_go' AND c.updated_at >= @since
  AND NOT EXISTS (SELECT 1 FROM project_copies later WHERE later.from_project_id = @project_id AND later.status = 'go' AND later.id > c.id)
ORDER BY c.id DESC LIMIT 1;

-- name: CopyWentTo :one
-- The copy of a project that went and wasn't undone, while its new project's there.
SELECT * FROM project_copies WHERE from_project_id = ? AND status = 'go' AND undone_at IS NULL AND project_id IS NOT NULL ORDER BY id DESC LIMIT 1;

-- name: CopyCameFrom :one
-- The copy that made a project, while its old project's there.
SELECT * FROM project_copies WHERE project_id = ? AND status = 'go' AND undone_at IS NULL AND from_project_id IS NOT NULL ORDER BY id DESC LIMIT 1;

-- name: LatestDeletion :one
SELECT * FROM project_deletions WHERE project_id = ? ORDER BY id DESC LIMIT 1;

-- name: LastGoBackup :one
SELECT * FROM backup_runs WHERE project_id = ? AND operation = 'backup' AND status = 'go' ORDER BY id DESC LIMIT 1;

-- name: LiveLocations :many
-- Where a volume can live: set up, and a share or a folder.
SELECT * FROM storage_locations WHERE acknowledged_at IS NOT NULL AND kind IN ('nfs', 'local') ORDER BY name;

-- name: SavedSecrets :many
SELECT * FROM secrets WHERE project_id = ? ORDER BY key;
