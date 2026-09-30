-- name: APITokenByDigest :one
SELECT * FROM api_tokens WHERE token_digest = ?;

-- name: MarkTokenUsed :exec
-- At most one write a minute, however busy the token is.
UPDATE api_tokens SET last_used_at = @now WHERE id = @id AND (last_used_at IS NULL OR last_used_at < @before);
