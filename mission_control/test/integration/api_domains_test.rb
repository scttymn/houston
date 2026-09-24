require "test_helper"
require_relative "../support/api_helpers"
require_relative "../support/cloudflare_stubs"
require_relative "../support/project_helpers"

class ApiDomainsTest < ActionDispatch::IntegrationTest
  include ApiHelpers
  include CloudflareStubs
  include ProjectHelpers

  setup do
    Installation.current.update!(dns_mode: "wildcard", cloudflare_zone_id: ZONE, tunnel_id: TUNNEL, cloudflare_api_token: TOKEN)
    # garage has served, so its domains are pointed on every sync (a first
    # deploy's wait for GO is api_sync_test's).
    make_deploy(make_project("garage"), 1, "go")
  end

  def zone(name, result) = cf(:get, "/zones", query: { "name" => name }, result:)
  def records(zone_id, name, result) = cf(:get, "/zones/#{zone_id}/dns_records", query: { "name" => name }, result:)
  def cname(name, project = "garage") = { type: "CNAME", name:, content: "#{TUNNEL}.cfargotunnel.com", proxied: true, comment: "managed-by:houston project:#{project}" }
  def states = json["domains"].transform_values { |v| v["state"] }
  def garage(domains) = equip_payload(name: "garage", services: %w[app], variables: [], domains:)

  test "custom domains get their records" do
    zone("equipping.com", [ { id: "z2", name: "equipping.com", status: "active" } ])
    zone("www.equipping.com", [])
    zone("rideclub.app", [ { id: "z3", name: "rideclub.app", status: "pending" } ])
    records("z2", "equipping.com", [])
    records("z2", "www.equipping.com", [ { id: "r9", type: "CNAME", comment: "managed-by:houston project:garage" } ])
    records("z3", "rideclub.app", [])
    apex = cf(:post, "/zones/z2/dns_records", result: { id: "r1" }).with(body: cname("equipping.com"))
    www = cf(:patch, "/zones/z2/dns_records/r9", result: { id: "r9" }).with(body: cname("www.equipping.com"))
    pending = cf(:post, "/zones/z3/dns_records", result: { id: "r3" }).with(body: cname("rideclub.app"))

    sync(garage(%w[equipping.com www.equipping.com rideclub.app]))

    assert_response :success
    assert_equal({ "equipping.com" => "DNS OK", "www.equipping.com" => "DNS OK", "rideclub.app" => "DNS PENDING" }, states)
    assert_requested apex
    assert_requested www
    assert_requested pending
    assert_equal "DNS PENDING", Project.find_by!(name: "garage").domain_states.dig("rideclub.app", "state")
  end

  test "domains Houston can't or mustn't point" do
    zone("nozone.dev", [])
    zone("legacy.equipping.com", [])
    zone("other.equipping.com", [])
    zone("equipping.com", [ { id: "z2", name: "equipping.com", status: "active" } ])
    records("z2", "legacy.equipping.com", [ { id: "old", type: "A", content: "192.0.2.1", comment: nil } ])
    records("z2", "other.equipping.com", [ { id: "theirs", type: "CNAME", comment: "managed-by:houston project:rideclub" } ])
    cf(:get, "/zones", query: { "name" => "broken.example.org" }, status: 500, errors: [ { code: 1000, message: "Internal error" } ])
    zone("fine.example.org", [])
    zone("example.org", [ { id: "z4", name: "example.org", status: "active" } ])
    records("z4", "fine.example.org", [])
    fine = cf(:post, "/zones/z4/dns_records", result: { id: "r4" }).with(body: cname("fine.example.org"))

    sync(garage(%w[nozone.dev legacy.equipping.com other.equipping.com broken.example.org fine.example.org]))

    assert_response :success
    assert_equal({ "nozone.dev" => "ZONE NOT IN CLOUDFLARE YET", "legacy.equipping.com" => "NO-GO", "other.equipping.com" => "NO-GO",
                   "broken.example.org" => "CAN'T CHECK", "fine.example.org" => "DNS OK" }, states)
    assert_match(/didn't create/, json.dig("domains", "legacy.equipping.com", "reason"))
    assert_match(/belongs to project rideclub/, json.dig("domains", "other.equipping.com", "reason"))
    assert_match(/Internal error/, json.dig("domains", "broken.example.org", "reason"))
    assert_requested fine
    assert_not_requested :post, %r{/zones/z2/dns_records}
    assert_not_requested :patch, %r{/dns_records/}
  end

  test "a dropped domain's record goes" do
    zone("keep.equipping.com", [])
    zone("old.equipping.com", [])
    zone("equipping.com", [ { id: "z2", name: "equipping.com", status: "active" } ])
    records("z2", "keep.equipping.com", [ { id: "rk", type: "CNAME", comment: "managed-by:houston project:garage" } ])
    records("z2", "old.equipping.com", [ { id: "ro", type: "CNAME", comment: "managed-by:houston project:garage" } ])
    cf(:patch, "/zones/z2/dns_records/rk", result: { id: "rk" })
    cf(:patch, "/zones/z2/dns_records/ro", result: { id: "ro" })
    sync(garage(%w[keep.equipping.com old.equipping.com]))
    assert_response :success

    gone = cf(:delete, "/zones/z2/dns_records/ro", result: { id: "ro" })
    sync(garage(%w[keep.equipping.com]))
    assert_response :success
    assert_requested gone
    assert_equal %w[keep.equipping.com], Project.find_by!(name: "garage").domain_states.keys

    # Somebody else's record at a dropped name is left alone.
    records("z2", "old.equipping.com", [ { id: "ro2", type: "CNAME", comment: "managed-by:houston project:rideclub" } ])
    sync(garage(%w[keep.equipping.com old.equipping.com]))
    sync(garage(%w[keep.equipping.com]))
    assert_not_requested :delete, %r{/dns_records/ro2}
  end

  test "domains under the base domain" do
    sync(garage(%w[api.svnmns.com]))
    assert_equal({ "api.svnmns.com" => "WILDCARD" }, states)
    assert_not_requested :any, /api\.cloudflare\.com/

    Installation.current.update!(dns_mode: "per_host")
    records(ZONE, "garage.svnmns.com", [])
    cf(:post, "/zones/#{ZONE}/dns_records", result: { id: "base" }).with(body: cname("garage.svnmns.com"))
    records(ZONE, "api.svnmns.com", [])
    api = cf(:post, "/zones/#{ZONE}/dns_records", result: { id: "api" }).with(body: cname("api.svnmns.com"))
    sync(garage(%w[api.svnmns.com]))
    assert_equal({ "api.svnmns.com" => "DNS OK" }, states)
    assert_requested api
  end
end
