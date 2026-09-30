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
puts "made #{ActiveRecord::Base.connection_db_config.database}: #{Installation.count} installation"
