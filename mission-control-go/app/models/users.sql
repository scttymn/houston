-- name: UserExists :one
SELECT EXISTS (SELECT 1 FROM users) AS present;

-- name: LiveRunners :one
-- Runners heard from in the last minute.
SELECT count(*) FROM runners WHERE last_seen_at >= ?;

-- name: DefaultStorageReady :one
SELECT EXISTS (SELECT 1 FROM storage_locations WHERE is_default AND acknowledged_at IS NOT NULL) AS ready;
