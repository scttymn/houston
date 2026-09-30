-- name: CreateLocation :one
INSERT INTO storage_locations (name, kind, settings, credentials, restic_password) VALUES (?, ?, ?, ?, ?) RETURNING *;

-- name: ResetLocation :one
-- A location set up again before it was confirmed: its password stays, so
-- what an earlier try created can still be opened.
UPDATE storage_locations SET kind = ?, settings = ?, credentials = ?, updated_at = ? WHERE id = ? RETURNING *;

-- name: SetLocationVerified :exec
UPDATE storage_locations SET verified_at = @now, updated_at = @now WHERE id = @id;

-- name: AcknowledgeLocation :exec
UPDATE storage_locations SET acknowledged_at = @now, updated_at = @now WHERE id = @id;

-- name: RecentUpdates :many
SELECT * FROM server_updates ORDER BY id DESC LIMIT 10;

-- name: UpdateByID :one
SELECT * FROM server_updates WHERE id = ?;
