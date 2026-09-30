-- name: UserExists :one
SELECT EXISTS (SELECT 1 FROM users) AS present;

-- name: LiveRunners :one
-- Runners heard from in the last minute.
SELECT count(*) FROM runners WHERE last_seen_at >= ?;

-- name: DefaultStorageReady :one
SELECT EXISTS (SELECT 1 FROM storage_locations WHERE is_default AND acknowledged_at IS NOT NULL) AS ready;

-- name: SetupCodes :many
SELECT id, code_digest FROM setup_codes;

-- name: ForgetSetupCodes :exec
DELETE FROM setup_codes;

-- name: AddSetupCode :exec
INSERT INTO setup_codes (code_digest) VALUES (?);

-- name: UseSetupCode :execrows
-- Exactly one request can use a code: the one whose delete removes it.
DELETE FROM setup_codes WHERE id = ?;

-- name: AddUser :one
INSERT INTO users (email_address, password_digest, created_at, updated_at) VALUES (?, ?, ?, ?) RETURNING id;
