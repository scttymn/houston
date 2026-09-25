require "test_helper"
require_relative "../support/cloudflare_settings_stubs"
require_relative "../support/project_helpers"

# Repair: the routes and Houston's records put back (docs/plans/cloudflare-settings.md, row 8).
class CloudflareRepairTest < ActiveSupport::TestCase
  include CloudflareSettingsStubs
  include ProjectHelpers

  setup do
    connect_host_by_host
    @equip = make_project("equip")
    make_deploy(@equip, 1, "go")
    @esther = make_project("estherpictures", domains: %w[estherpictures.com])
    make_deploy(@esther, 1, "go")
    make_project("fresh") # linked, never deployed: its name waits for its first GO
  end

  def stub_dns(existing)
    writes = []
    stub_request(:get, %r{#{API}/zones/[^/]+/dns_records\?name=}).to_return do |req|
      name = CGI.unescape(req.uri.query[/name=([^&]+)/, 1])
      { status: 200, headers: { "Content-Type" => "application/json" }, body: { success: true, errors: [], result: [ existing[name] ].compact }.to_json }
    end
    stub_request(:get, "#{API}/zones").with(query: hash_including("name")).to_return do |req|
      name = CGI.unescape(req.uri.query[/name=([^&]+)/, 1])
      zone = { "svnmns.com" => ZONE, "estherpictures.com" => OTHER_ZONE }[name]
      { status: 200, headers: { "Content-Type" => "application/json" }, body: { success: true, errors: [], result: zone ? [ { id: zone, name:, status: "active" } ] : [] }.to_json }
    end
    %i[post patch].each do |verb|
      stub_request(verb, %r{#{API}/zones/[^/]+/dns_records}).to_return do |req|
        writes << [ verb, JSON.parse(req.body)["name"], JSON.parse(req.body)["comment"] ]
        { status: 200, headers: { "Content-Type" => "application/json" }, body: { success: true, errors: [], result: {} }.to_json }
      end
    end
    writes
  end

  test "repair puts routes and records back" do
    pushes = record_pushes
    writes = stub_dns({ "admin.svnmns.com" => record("admin.svnmns.com", comment: "managed-by:houston"),
                        "equip.svnmns.com" => record("equip.svnmns.com", comment: "managed-by:houston project:equip"),
                        "hooks.svnmns.com" => record("hooks.svnmns.com", comment: nil, content: "somewhere-else") })
    results = CloudflareRepair.run
    assert_equal 1, pushes.size, "the routes pushed once"
    by_item = results.to_h { |r| [ r.item, r.state ] }
    assert_equal "OK", by_item["routes"]
    assert_equal "DNS OK", by_item["admin.svnmns.com"]
    assert_equal "NO-GO", by_item["hooks.svnmns.com"], "a record Houston didn't create"
    assert_equal "DNS OK", by_item["equip.svnmns.com"]
    assert_equal "DNS OK", by_item["estherpictures.svnmns.com"]
    assert_equal "DNS OK", by_item["estherpictures.com"]
    assert_equal({ "estherpictures.com" => { "state" => "DNS OK", "reason" => nil } }, @esther.reload.domain_states, "the project page's states too")
    assert_not by_item.key?("fresh.svnmns.com"), "never deployed: pointed at its first GO"
    assert_not writes.any? { |_, name, _| name == "hooks.svnmns.com" }, "the foreign record wasn't touched"
    assert_includes writes, [ :patch, "equip.svnmns.com", "managed-by:houston project:equip" ]
    assert_includes writes, [ :post, "estherpictures.svnmns.com", "managed-by:houston project:estherpictures" ]
  end
end
