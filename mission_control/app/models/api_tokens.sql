-- name: APITokenByDigest :one
SELECT * FROM api_tokens WHERE token_digest = ?;

-- name: MarkTokenUsed :exec
-- At most one write a minute, however busy the token is.
UPDATE api_tokens SET last_used_at = @now WHERE id = @id AND (last_used_at IS NULL OR last_used_at < @before);

-- name: AllTokens :many
SELECT * FROM api_tokens ORDER BY name;

-- name: CreateToken :one
INSERT INTO api_tokens (name, token_digest) VALUES (?, ?) RETURNING *;

-- name: DeleteToken :execrows
DELETE FROM api_tokens WHERE id = ?;

-- name: TokenNameTaken :one
SELECT EXISTS (SELECT 1 FROM api_tokens WHERE name = ?) AS taken;
