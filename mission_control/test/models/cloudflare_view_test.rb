require "test_helper"
require_relative "../support/cloudflare_settings_stubs"
require_relative "../support/project_helpers"

# What Cloudflare has, for Settings (docs/plans/cloudflare-settings.md, rows 1-4).
class CloudflareViewTest < ActiveSupport::TestCase
  include CloudflareSettingsStubs
  include ProjectHelpers

  setup do
    connect_host_by_host
    stub_zones
    stub_records(ZONE, [])
    stub_records(OTHER_ZONE, [])
  end

  test "the tunnel and its connections" do
    stub_tunnel_details
    stub_live_ingress
    view = CloudflareView.fetch
    assert_equal [ "houston-svnmns", TUNNEL, "healthy" ], [ view.tunnel.name, view.tunnel.id, view.tunnel.status ]
    assert_equal 4, view.connections.size
    c = view.connections.first
    assert_equal [ "MCI01", "2026.9.1", "99.98.226.252", Time.utc(2026, 9, 24, 20, 10, 1) ], [ c.colo, c.version, c.origin, c.since ]
    assert_empty view.problems
  end

  test "live routes, and drift" do
    stub_tunnel_details
    stub_live_ingress
    view = CloudflareView.fetch
    assert_equal [ "admin.svnmns.com", "hooks.svnmns.com", "hooks.svnmns.com", nil ], view.routes.map(&:hostname)
    assert_not view.drift?, "the live routes are the database's"

    edited = expected_ingress[:config][:ingress].dup
    edited[0] = { hostname: "admin.svnmns.com", service: "http://elsewhere:80" }
    stub_live_ingress(edited)
    view = CloudflareView.fetch
    assert view.drift?
    assert view.routes.first.drift, "the edited rule"
    assert_equal [ "admin.svnmns.com" ], view.missing_routes.map(&:hostname), "the database's rule Cloudflare lacks"
  end

  test "Houston's records, and another server's" do
    stub_tunnel_details
    stub_live_ingress
    stub_records(ZONE, [
      record("admin.svnmns.com", comment: "managed-by:houston"),
      record("equip.svnmns.com", comment: "managed-by:houston project:equip"),
      record("other.svnmns.com", comment: "managed-by:houston project:other", content: "ffff0000-second-box.cfargotunnel.com"),
      record("git.svnmns.com", comment: nil, content: "192.0.2.1")
    ])
    stub_records(OTHER_ZONE, [ record("estherpictures.com", comment: "managed-by:houston project:estherpictures") ])
    rows = CloudflareView.fetch.records.to_h { |r| [ r.name, [ r.zone, r.project, r.here ] ] }
    assert_equal({ "admin.svnmns.com" => [ "svnmns.com", "admin", true ],
                   "equip.svnmns.com" => [ "svnmns.com", "equip", true ],
                   "other.svnmns.com" => [ "svnmns.com", "other", false ],
                   "estherpictures.com" => [ "estherpictures.com", "estherpictures", true ] }, rows)
  end

  test "a failing Cloudflare is a reason, not an error" do
    stub_tunnel_details(status: 403)
    stub_request(:get, "#{API}/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/configurations").to_timeout
    view = CloudflareView.fetch
    assert_nil view.tunnel
    assert_empty view.routes
    assert_match(/tunnel: Authentication error/, view.problems.join("\n"))
    assert_match(/routes: couldn't reach Cloudflare/, view.problems.join("\n"))
    assert_equal 0, view.records.size, "the records part still ran (no managed records)"
  end

  # Settings shows the last answer at once (no waiting, no empty panel), and
  # refreshes it in the background once it's old.
  test "the last answer is kept, and a failed check never replaces a good one" do
    Rails.cache.clear
    assert_nil CloudflareView.last

    stub_tunnel_details
    stub_live_ingress
    fresh = CloudflareView.fetch
    kept = CloudflareView.last
    assert_equal fresh.to_h, kept.to_h
    assert_in_delta Time.current, kept.checked_at, 1
    assert_not kept.stale?
    travel CloudflareView::STALE_AFTER + 1.second
    assert CloudflareView.last.stale?

    WebMock.reset!
    stub_tunnel_details(status: 403)
    stub_request(:get, %r{#{API}/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/configurations}).to_timeout
    stub_zones([])
    failed = CloudflareView.fetch
    assert failed.problems.any?
    assert_equal fresh.to_h, CloudflareView.last.to_h, "the good answer stays"
    assert_equal fresh.tunnel, failed.tunnel, "and what failed is shown over it"

    Installation.current.update!(tunnel_id: "another-tunnel")
    assert_nil CloudflareView.last, "a new tunnel's answer isn't the old one's"
  ensure
    Rails.cache.clear
  end
end
