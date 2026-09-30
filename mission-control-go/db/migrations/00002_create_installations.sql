-- This server's Houston setup: one row (id 1). The Cloudflare tokens are
-- crypt.String (sqlc.yaml), encrypted at rest.
-- +goose Up
CREATE TABLE installations (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  base_domain TEXT NOT NULL DEFAULT '',
  time_zone TEXT NOT NULL DEFAULT 'UTC',
  dns_mode TEXT NOT NULL DEFAULT '',
  port_open BOOLEAN NOT NULL DEFAULT TRUE,
  cloudflare_account_id TEXT NOT NULL DEFAULT '',
  cloudflare_zone_id TEXT NOT NULL DEFAULT '',
  cloudflare_api_token TEXT NOT NULL DEFAULT '',
  cloudflare_connected_at DATETIME,
  tunnel_id TEXT NOT NULL DEFAULT '',
  tunnel_token TEXT NOT NULL DEFAULT '',
  latest_release TEXT NOT NULL DEFAULT '',
  latest_release_url TEXT NOT NULL DEFAULT '',
  latest_release_checked_at DATETIME,
  registry_cleanup_since DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE installations;
