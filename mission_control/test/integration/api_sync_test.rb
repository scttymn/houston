require "test_helper"
require_relative "../support/api_helpers"
require_relative "../support/cloudflare_stubs"

class ApiSyncTest < ActionDispatch::IntegrationTest
  include ApiHelpers
  include CloudflareStubs

  test "sync creates the project" do
    sync(equip_payload(variables: [ { name: "RAILS_MASTER_KEY", required: false }, { name: "SENTRY_DSN", required: false } ]))

    assert_response :success
    assert_equal({ "project" => "equip", "host" => "equip.svnmns.com", "dns" => "wildcard", "domains" => {} }, json)
    project = Project.find_by!(name: "equip")
    assert_equal "app", project.app_service
    assert_equal %w[app db], project.services
    assert_equal [], project.domains
    assert_equal [ { "name" => "RAILS_MASTER_KEY", "required" => false }, { "name" => "SENTRY_DSN", "required" => false } ], project.variables
    assert_equal "/up", project.health
    assert_equal 80, project.port
    assert_equal({ "on" => "commit", "branch" => "main", "tags" => "v*" }, project.deploy_rule)
    assert project.synced_at
    assert_equal %w[equip equip-db], project.hosts.pluck(:name).sort
  end

  # Build step 5: what a backup holds. Absent (an older sync) is none.
  test "sync records the data to back up" do
    optional = [ { name: "RAILS_MASTER_KEY", required: false } ]
    sync(equip_payload(variables: optional, volumes: [ { name: "storage", path: "/rails/storage" } ], databases: [ { service: "db", image: "postgres:17" } ]))
    assert_response :success
    project = Project.find_by!(name: "equip")
    assert_equal [ { "name" => "storage", "path" => "/rails/storage" } ], project.volumes
    assert_equal [ { "service" => "db", "image" => "postgres:17" } ], project.databases

    sync(equip_payload(variables: optional))
    assert_response :success
    assert_equal [], project.reload.volumes
    assert_equal [], project.databases

    project.update!(volumes: [ { "name" => "keep", "path" => "/keep" } ])
    {
      "volumes" => [ { volumes: "storage" }, { volumes: [ { name: "storage", path: "rails/storage" } ] },
                     { volumes: [ { name: "Bad Name", path: "/x" } ] }, { volumes: [ { name: "storage" } ] },
                     { volumes: Array.new(51) { |i| { name: "v#{i}", path: "/v#{i}" } } } ],
      "databases" => [ { databases: [ { service: "pg", image: "postgres:17" } ] }, { databases: [ { service: "db" } ] }, { databases: {} } ]
    }.each do |field, cases|
      cases.each do |change|
        sync(equip_payload(**change))
        assert_response :unprocessable_entity, change.inspect
        assert_includes json["errors"].keys, field, "#{change.inspect}: #{json}"
      end
    end
    assert_equal [ { "name" => "keep", "path" => "/keep" } ], project.reload.volumes
  end

  test "sync rejects what the CLI would never send" do
    {
      "name" => [ { name: "Equip" }, { name: "admin" }, { name: "hooks" }, { name: "-x" }, { name: nil } ],
      "services" => [ { services: "app" }, { services: [ "app", "Bad Name" ] } ],
      "app_service" => [ { app_service: "web" } ],
      "variables" => [ { variables: [ { name: "my.key", required: true } ] }, { variables: "RAILS_MASTER_KEY" } ],
      "domains" => [ { domains: [ "https://equip.com" ] }, { domains: [ "*.equip.com" ] } ],
      "port" => [ { port: 0 }, { port: 70000 }, { port: "80" } ],
      "health" => [ { health: "up" }, { health: "/u p" } ]
    }.each do |field, cases|
      cases.each do |change|
        sync(equip_payload(**change))
        assert_response :unprocessable_entity, change.inspect
        assert_includes json["errors"].keys, field, "#{change.inspect}: #{json}"
      end
    end

    sync("{not json")
    assert_response :bad_request

    sync(equip_payload(domains: Array.new(3000) { |i| "d#{i}.example-domain.com" }))
    assert_response :content_too_large

    assert_equal 0, Project.count
  end

  test "sync again updates in place" do
    sync(equip_payload(variables: []))
    sync(equip_payload(variables: [], services: %w[app cache], health: "/health", port: 3000))

    assert_response :success
    assert_equal 1, Project.count
    project = Project.sole
    assert_equal %w[app cache], project.services
    assert_equal [ "/health", 3000 ], [ project.health, project.port ]
    assert_equal %w[equip equip-cache], project.hosts.pluck(:name).sort
  end

  test "sync holds for missing required secrets" do
    sync
    assert_response :unprocessable_entity
    assert_equal [ { "name" => "RAILS_MASTER_KEY", "required" => true }, { "name" => "SENTRY_DSN", "required" => false } ],
                 Project.find_by!(name: "equip").variables
    assert_equal [ "RAILS_MASTER_KEY" ], json["missing"]
    assert_match(/RAILS_MASTER_KEY/, json["error"])

    Project.find_by!(name: "equip").secrets.create!(key: "RAILS_MASTER_KEY", value: "abc123")
    sync
    assert_response :success
  end

  test "sync refuses container names another project owns" do
    Project.find_or_create_by!(name: "keeper") { |p| p.assign_attributes(app_service: "app", services: %w[app], health: "/up", port: 80) }

    [
      [ equip_payload(name: "shop", services: %w[app db], variables: []), equip_payload(name: "shop-db", services: %w[app], variables: []), "shop-db" ],
      [ equip_payload(name: "shop2-db", services: %w[app], variables: []), equip_payload(name: "shop2", services: %w[app db], variables: []), "shop2-db" ],
      [ equip_payload(name: "a", services: %w[app b-c], variables: []), equip_payload(name: "a-b", services: %w[app c], variables: []), "a-b-c" ]
    ].each do |first, second, clash|
      sync(first)
      assert_response :success, first.inspect
      owner = Project.find_by!(name: first[:name])
      before = owner.hosts.pluck(:name).sort

      sync(second)
      assert_response :unprocessable_entity, second.inspect
      assert_match(/#{Regexp.escape(clash)}.*#{Regexp.escape(first[:name])}/, json["error"])
      assert_nil Project.find_by(name: second[:name])
      assert_equal before, owner.reload.hosts.pluck(:name).sort
    end
  end

  test "sync points <name>.<base> in host-by-host mode" do
    record = { type: "CNAME", name: "equip.svnmns.com", content: "#{TUNNEL}.cfargotunnel.com", proxied: true, comment: "managed-by:houston project:equip" }

    Installation.current.update!(dns_mode: "per_host", cloudflare_zone_id: ZONE, tunnel_id: TUNNEL, cloudflare_api_token: TOKEN)
    cf(:get, "/zones/#{ZONE}/dns_records", query: { "name" => "equip.svnmns.com" }, result: [])
    create = cf(:post, "/zones/#{ZONE}/dns_records", result: { id: "rec1" }).with(body: record)
    sync(equip_payload(variables: []))
    assert_response :success
    assert_equal "per_host", json["dns"]
    assert_requested create

    WebMock.reset!
    cf(:get, "/zones/#{ZONE}/dns_records", query: { "name" => "equip.svnmns.com" }, result: [ { id: "rec1", type: "CNAME", comment: "managed-by:houston project:equip" } ])
    update = cf(:patch, "/zones/#{ZONE}/dns_records/rec1", result: { id: "rec1" }).with(body: record)
    sync(equip_payload(variables: []))
    assert_response :success
    assert_requested update

    WebMock.reset!
    cf(:get, "/zones/#{ZONE}/dns_records", query: { "name" => "equip.svnmns.com" }, result: [ { id: "old", type: "A", content: "192.0.2.1", comment: nil } ])
    sync(equip_payload(variables: []))
    assert_response :unprocessable_entity
    assert_match(/equip\.svnmns\.com already exists.*managed-by:houston/, json["error"])
    assert_not_requested :post, %r{/dns_records}
    assert_not_requested :patch, %r{/dns_records}

    WebMock.reset!
    Installation.current.update!(dns_mode: "wildcard")
    sync(equip_payload(variables: []))
    assert_response :success
    assert_equal "wildcard", json["dns"]
    assert_not_requested :any, /api\.cloudflare\.com/
  end

  test "a Cloudflare failure during sync can be retried" do
    Installation.current.update!(dns_mode: "per_host", cloudflare_zone_id: ZONE, tunnel_id: TUNNEL, cloudflare_api_token: TOKEN)
    cf(:get, "/zones/#{ZONE}/dns_records", query: { "name" => "equip.svnmns.com" }, status: 500, errors: [ { code: 1000, message: "Internal error" } ])

    sync(equip_payload(variables: []))
    assert_response :bad_gateway
    assert_match(/Internal error/, json["error"])
    assert Project.find_by(name: "equip")

    WebMock.reset!
    cf(:get, "/zones/#{ZONE}/dns_records", query: { "name" => "equip.svnmns.com" }, result: [])
    create = cf(:post, "/zones/#{ZONE}/dns_records", result: { id: "rec1" })
    sync(equip_payload(variables: []))
    assert_response :success
    assert_requested create
  end
end
