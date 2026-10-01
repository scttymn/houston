-- name: ForgetOldLinks :exec
DELETE FROM repo_links WHERE created_at < ?;

-- name: CreateLink :one
INSERT INTO repo_links (repo_url, deploy_key_private, deploy_key_public, webhook_secret, created_at, updated_at)
VALUES (@repo_url, @deploy_key_private, @deploy_key_public, @webhook_secret, @now, @now) RETURNING *;

-- name: LinkByID :one
SELECT * FROM repo_links WHERE id = ?;

-- name: SetLinkRead :exec
UPDATE repo_links SET branch = ?, compose_path = ?, preview = ?, preview_sha = ?, updated_at = ? WHERE id = ?;

-- name: DeleteLink :exec
DELETE FROM repo_links WHERE id = ?;
