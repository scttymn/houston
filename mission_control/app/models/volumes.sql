-- name: EnsureProjectVolume :exec
-- The unique (project, name) settles two syncs at once.
INSERT INTO project_volumes (project_id, name) VALUES (?, ?) ON CONFLICT (project_id, name) DO NOTHING;

-- name: ProjectVolumeByName :one
SELECT * FROM project_volumes WHERE project_id = ? AND name = ?;

-- name: MarkVolumePlaced :exec
UPDATE project_volumes SET placed_at = ?, updated_at = ? WHERE id = ? AND placed_at IS NULL;

-- name: StorageLocationByID :one
SELECT * FROM storage_locations WHERE id = ?;

-- name: LiveStorageLocations :many
-- The locations that hold live volumes: an NFS export, a folder.
SELECT * FROM storage_locations WHERE kind IN ('nfs', 'local') ORDER BY id;
