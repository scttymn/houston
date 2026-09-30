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

-- name: SetupCandidate :one
-- The location setup's storage step made: tested, its password not yet
-- confirmed saved.
SELECT * FROM storage_locations WHERE acknowledged_at IS NULL AND verified_at IS NOT NULL ORDER BY id DESC LIMIT 1;

-- name: FinishStorageSetup :exec
-- Its password saved: the location confirmed, and the default.
UPDATE storage_locations SET is_default = (id = @id),
  acknowledged_at = CASE WHEN id = @id THEN @now ELSE acknowledged_at END,
  updated_at = CASE WHEN id = @id THEN @now ELSE updated_at END;
