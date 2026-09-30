-- name: InstallationBaseDomain :one
-- The base domain setup chose ("" before it did); no row before setup.
SELECT base_domain FROM installations WHERE id = 1;

-- name: InstallationConnected :one
-- Whether setup has connected Cloudflare (no row before setup).
SELECT cloudflare_connected_at IS NOT NULL AS connected FROM installations WHERE id = 1;

-- name: CurrentInstallation :one
SELECT * FROM installations WHERE id = 1;

-- name: RegistryCleaning :one
-- The registry's garbage collection holds this lock while it runs; one
-- older than @since was left by a Mission Control that stopped.
SELECT EXISTS (SELECT 1 FROM installations WHERE registry_cleanup_since >= @since) AS cleaning;

-- name: SetLatestRelease :exec
UPDATE installations SET latest_release = ?, latest_release_url = ?, latest_release_checked_at = ?, updated_at = ? WHERE id = 1;
