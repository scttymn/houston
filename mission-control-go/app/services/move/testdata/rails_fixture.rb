# The Rails app's database for the move's tests, made by the Rails app
# itself (its schema, its encryption, with its development keys). Each batch
# adds the rows of the tables it moves. Run from mission_control/:
#
#   rm -f tmp/rails.sqlite3
#   docker compose run --rm --no-deps -T -e DATABASE_URL=sqlite3:tmp/rails.sqlite3 app \
#     sh -c 'bin/rails db:schema:load && bin/rails runner -' < ../mission-control-go/app/services/move/testdata/rails_fixture.rb
#   cp tmp/rails.sqlite3 ../mission-control-go/app/services/move/testdata/
Installation.create!(
  base_domain: "houston.example",
  time_zone: "America/Denver",
  dns_mode: "tunnel",
  port_open: false,
  cloudflare_account_id: "acct-1234",
  cloudflare_zone_id: "zone-5678",
  cloudflare_api_token: "cf-api-token-café",
  cloudflare_connected_at: Time.utc(2026, 9, 1, 12, 30, 15),
  tunnel_id: "tunnel-9abc",
  tunnel_token: "tunnel-token-" + "x" * 300,
  latest_release: "v0.4.27",
  latest_release_url: "https://github.com/scttymn/houston/releases/tag/v0.4.27",
  latest_release_checked_at: Time.utc(2026, 9, 30, 6, 0, 0),
  registry_cleanup_since: nil,
  created_at: Time.utc(2026, 1, 2, 3, 4, 5),
  updated_at: Time.utc(2026, 9, 30, 6, 0, 0)
)

# Storage: an NFS share (the default) and S3, whose credentials are JSON,
# encrypted.
nas = StorageLocation.create!(name: "nas", kind: "nfs", settings: { "server" => "192.168.0.10", "export" => "/volume1/houston" },
                              restic_password: "restic-pässword", default: true,
                              acknowledged_at: Time.utc(2026, 2, 1), verified_at: Time.utc(2026, 2, 1), pruned_at: Time.utc(2026, 9, 29, 4))
StorageLocation.create!(name: "offsite", kind: "s3", settings: { "bucket" => "houston-backups", "endpoint" => "s3.example.com" },
                        credentials: { "access_key_id" => "AKIA123", "secret_access_key" => "s3cr3t/+=" }, restic_password: "r" * 200,
                        verified_at: Time.utc(2026, 3, 1), prune_error: "the bucket said no")

# Projects: one with everything set, one as a first sync leaves it.
shop = Project.create!(
  name: "shop", app_service: "web", services: %w[web db], domains: %w[shop.houston.example shop.example.com],
  domain_states: { "shop.example.com" => { "state" => "OK", "reason" => "points at the tunnel" } },
  variables: [ { "name" => "SECRET_KEY_BASE", "required" => true }, { "name" => "SENTRY_DSN", "required" => false } ],
  volumes: [ { "name" => "data", "path" => "/rails/storage" } ], databases: [ { "service" => "db", "image" => "postgres:17" } ],
  details: { "images" => { "db" => "postgres:17" }, "cpus" => "2", "console" => "bin/rails console" },
  deploy_rule: { "on" => "tag", "tags" => "v*" }, health: "/up", port: 3000, data_generation: 2,
  keep_auto: 7, keep_deploy: 5, backup_schedule: "daily 04:30", chosen_backup_location: nas,
  repo_url: "git@github.com:scttymn/shop.git", branch: "main", compose_path: "compose.yml",
  deploy_key_private: "-----BEGIN OPENSSH PRIVATE KEY-----\n#{"k" * 400}\n-----END OPENSSH PRIVATE KEY-----\n",
  deploy_key_public: "ssh-ed25519 AAAA shop", webhook_secret: "whsec-shop", webhook_verified_at: Time.utc(2026, 5, 1),
  seen_refs: { "refs/heads/main" => "a" * 40 }, last_checked_at: Time.utc(2026, 9, 30, 5), last_check_error: nil,
  maintenance_since: Time.utc(2026, 9, 30, 7), maintenance_by: "scotty", maintenance_message: "Back at noon",
  maintenance_page: "<h1>Closed</h1>", synced_at: Time.utc(2026, 9, 30, 6, 30), created_at: Time.utc(2026, 1, 5))
blog = Project.create!(name: "blog", app_service: "web", services: %w[web], domains: [], variables: [], health: "/", port: 80)

%w[shop shop-db-g2].each { |name| shop.hosts.create!(name:) }
blog.hosts.create!(name: "blog")
shop.secrets.create!(key: "SECRET_KEY_BASE", value: "s3cret-ünïcode")
shop.secrets.create!(key: "SENTRY_DSN", value: "https://key@sentry.example/1" + "0" * 200)
ProjectVolume.create!(project: shop, name: "data", location: nas, placed_at: Time.utc(2026, 2, 2))

Runner.create!(name: "houston-runner-1", last_seen_at: Time.utc(2026, 9, 30, 8))
shop.deploys.create!(number: 1, sha: "a" * 40, ref: "refs/tags/v1", status: "go", step: "Post-deploy", runner: "houston-runner-1",
                     log: "Test ok\nBuild ok\n", token_digest: Deploy.digest("one"), heartbeat_at: Time.utc(2026, 9, 1, 10),
                     finished_at: Time.utc(2026, 9, 1, 10, 5), created_at: Time.utc(2026, 9, 1, 10))
shop.deploys.create!(number: 2, sha: "b" * 40, ref: "refs/restore/abcd1234", kind: "restore", status: "in_flight", generation: 2,
                     step: "Switch", runner: "houston-runner-1", token_digest: Deploy.digest("two"), heartbeat_at: Time.utc(2026, 9, 30, 8),
                     switched_at: Time.utc(2026, 9, 30, 8), source_location: nas, source_snapshot_id: "abcd1234" * 8,
                     sync_payload: { "name" => "shop", "port" => 3000 })
blog.deploys.create!(number: 1, sha: "c" * 40, ref: "refs/heads/main", status: "queued", fresh: true, token_digest: "",
                     heartbeat_at: Time.utc(2026, 9, 30, 8))

# Backups: a scheduled snapshot that went, and a deploy's that didn't.
BackupRun.create!(project: shop, location: nas, kind: "auto", reason: "schedule", scheduled_for: Date.new(2026, 9, 30), status: "go",
                  sha: "a" * 40, snapshot_id: "5eed" * 16, bytes: 123_456_789, found: { "volumes" => [ "data" ], "databases" => [ "db" ] },
                  log: "restic backup\n", heartbeat_at: Time.utc(2026, 9, 30, 3, 1), started_at: Time.utc(2026, 9, 30, 3),
                  finished_at: Time.utc(2026, 9, 30, 3, 2))
BackupRun.create!(project: shop, location: nas, kind: "deploy", reason: "deploy", deploy_number: 1, status: "no_go",
                  error: "restic: repository is locked", heartbeat_at: Time.utc(2026, 9, 1, 10, 1))

# Personal tokens: laptop's is known, so its move can be checked.
ApiToken.create!(name: "laptop", token_digest: ApiToken.digest("hou_laptop-token"), last_used_at: Time.utc(2026, 9, 30, 9))
ApiToken.issue!("agent")

# Add project, midway: a draft that has read its compose.yml.
RepoLink.create!(repo_url: "git@github.com:scttymn/new.git", deploy_key_private: "-----NEW KEY-----", deploy_key_public: "ssh-ed25519 AAAA new",
                 webhook_secret: "new-whsec", preview: { "sync" => { "name" => "new" } }, preview_sha: "c" * 40)

puts "made #{ActiveRecord::Base.connection_db_config.database}: #{Installation.count} installation, #{Project.count} projects, " \
     "#{Deploy.count} deploys, #{Secret.count} secrets, #{StorageLocation.count} storage locations, #{BackupRun.count} backup runs, #{ApiToken.count} API tokens, #{RepoLink.count} drafts"
