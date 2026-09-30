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

-- name: SetPortOpen :exec
UPDATE installations SET port_open = ?, updated_at = ?;

-- name: SetCloudflareToken :exec
UPDATE installations SET cloudflare_api_token = ?, updated_at = ?;

-- name: ConnectCloudflare :exec
-- Setup's Cloudflare step, done: the installation's first row, or the one
-- a rerun finishes.
INSERT INTO installations (id, base_domain, cloudflare_account_id, cloudflare_zone_id, tunnel_id, cloudflare_api_token, tunnel_token, dns_mode,
  cloudflare_connected_at, updated_at)
VALUES (1, @base_domain, @cloudflare_account_id, @cloudflare_zone_id, @tunnel_id, @cloudflare_api_token, @tunnel_token, @dns_mode, @connected_at, @connected_at)
ON CONFLICT (id) DO UPDATE SET base_domain = excluded.base_domain, cloudflare_account_id = excluded.cloudflare_account_id,
  cloudflare_zone_id = excluded.cloudflare_zone_id, tunnel_id = excluded.tunnel_id, cloudflare_api_token = excluded.cloudflare_api_token,
  tunnel_token = excluded.tunnel_token, dns_mode = excluded.dns_mode, cloudflare_connected_at = excluded.cloudflare_connected_at,
  updated_at = excluded.updated_at;
