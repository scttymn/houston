# The data both versions start from in the parity check (bin/parity), made
# by the Rails app: setup finished (DNS by wildcard, so Cloudflare isn't
# asked), backup storage, shop deployed once and blog queued. No volumes:
# the Rails app would make real ones.
Installation.create!(base_domain: "houston.localhost", time_zone: "UTC", cloudflare_account_id: "acct", cloudflare_zone_id: "zone",
                     tunnel_id: "tun", cloudflare_api_token: "cf-token", tunnel_token: "tunnel-token", cloudflare_connected_at: Time.utc(2026, 9, 1))
StorageLocation.create!(name: "nas", kind: "nfs", settings: { "server" => "10.0.1.20", "export" => "/volume1/houston" },
                        restic_password: "restic", default: true, acknowledged_at: Time.utc(2026, 9, 1), verified_at: Time.utc(2026, 9, 1))
shop = Project.create!(name: "shop", app_service: "web", services: %w[web db], domains: [ "shop.houston.localhost" ],
                       variables: [ { "name" => "SECRET_KEY_BASE", "required" => true } ], health: "/up", port: 3000,
                       webhook_secret: "whsec", synced_at: Time.utc(2026, 9, 1))
shop.hosts.create!(name: "shop")
shop.hosts.create!(name: "shop-db")
shop.secrets.create!(key: "SECRET_KEY_BASE", value: "s3cret")
shop.deploys.create!(number: 1, sha: "a" * 40, ref: "refs/heads/main", status: "go", token_digest: Deploy.digest("old"),
                     heartbeat_at: Time.utc(2026, 9, 1), finished_at: Time.utc(2026, 9, 1))
blog = Project.create!(name: "blog", app_service: "web", services: %w[web], domains: [], variables: [], health: "/", port: 80,
                       repo_url: "git@github.com:scttymn/blog.git", branch: "main", compose_path: "compose.yml", deploy_key_private: "-----KEY-----")
blog.hosts.create!(name: "blog")
blog.deploys.create!(number: 1, sha: "b" * 40, ref: "refs/heads/main", status: "queued", token_digest: "", heartbeat_at: Time.utc(2026, 9, 1))
ApiToken.create!(name: "parity", token_digest: ApiToken.digest("hou_parity-personal-token"))
# Who signs in to look at the pages side by side (bin/parity --serve).
User.create!(email_address: "admin@houston.localhost", password: "parity-password")
puts "parity data: #{Project.count} projects, #{Deploy.count} deploys"
