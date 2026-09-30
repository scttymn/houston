-- name: InstallationBaseDomain :one
-- The base domain setup chose ("" before it did); no row before setup.
SELECT base_domain FROM installations WHERE id = 1;

-- name: InstallationConnected :one
-- Whether setup has connected Cloudflare (no row before setup).
SELECT cloudflare_connected_at IS NOT NULL AS connected FROM installations WHERE id = 1;

-- name: CurrentInstallation :one
SELECT * FROM installations WHERE id = 1;
