require "test_helper"
require_relative "../support/api_helpers"
require_relative "../support/cloudflare_stubs"
require_relative "../support/fake_docker"
require_relative "../support/project_helpers"

class ApiSyncTest < ActionDispatch::IntegrationTest
  include ApiHelpers
  include CloudflareStubs
  include FakeDockerHelper
  include ProjectHelpers

  def restore_in_flight(project, generation: 2)
    restore, token, = Deploy.start!(project, sha: "a" * 40, ref: "refs/restore/33333333")
    restore.update!(kind: "restore", generation:)
    [ restore, token ]
  end

  test "sync creates the project" do
    sync(equip_payload(variables: [ { name: "RAILS_MASTER_KEY", required: false }, { name: "SENTRY_DSN", required: false } ]))

    assert_response :success
    assert_equal({ "project" => "equip", "host" => "equip.svnmns.com", "dns" => "wildcard", "domains" => {}, "generation" => 1 }, json)
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
    placing = FakeDocker.new { |args| args[0..1] == %w[volume inspect] ? DockerCommand::Result.new(success: true, output: "null\n") : nil }
    use_fake_docker(placing) { sync(equip_payload(variables: optional, volumes: [ { name: "storage", path: "/rails/storage" } ], databases: [ { service: "db", image: "postgres:17" } ])) }
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

  test "sync records the keep rules" do
    optional = [ { name: "RAILS_MASTER_KEY", required: false } ]
    sync(equip_payload(variables: optional, backups: { keep_auto: 3, keep_deploy: 5 }))
    assert_response :success
    project = Project.find_by!(name: "equip")
    assert_equal [ 3, 5 ], [ project.keep_auto, project.keep_deploy ]

    sync(equip_payload(variables: optional))
    assert_equal [ 14, 10 ], [ project.reload.keep_auto, project.keep_deploy ]

    [ { keep_auto: 0, keep_deploy: 5 }, { keep_auto: 3, keep_deploy: 1001 }, { keep_auto: "5", keep_deploy: 5 }, { keep_auto: 3 }, "14/10" ].each do |backups|
      sync(equip_payload(variables: optional, backups:))
      assert_response :unprocessable_entity, backups.inspect
      assert_includes json["errors"].keys, "backups"
    end
    assert_equal [ 14, 10 ], [ project.reload.keep_auto, project.keep_deploy ]
  end

  test "sync records the schedule" do
    optional = [ { name: "RAILS_MASTER_KEY", required: false } ]
    sync(equip_payload(variables: optional, backups: { schedule: "daily 22:15", keep_auto: 14, keep_deploy: 10 }))
    assert_response :success
    project = Project.find_by!(name: "equip")
    assert_equal "daily 22:15", project.backup_schedule

    sync(equip_payload(variables: optional))
    assert_equal "daily 03:00", project.reload.backup_schedule

    [ "daily 3:00", "hourly", "daily 24:00", 300 ].each do |schedule|
      sync(equip_payload(variables: optional, backups: { schedule:, keep_auto: 14, keep_deploy: 10 }))
      assert_response :unprocessable_entity, schedule.inspect
      assert_includes json["errors"].keys, "backups"
    end
  end

  test "sync places the volumes" do
    optional = [ { name: "RAILS_MASTER_KEY", required: false } ]
    missing = FakeDocker.new { |args| args[0..1] == %w[volume inspect] ? failure("Error response from daemon: get equip_storage: no such volume\n") : nil }
    use_fake_docker(missing) { sync(equip_payload(variables: optional, volumes: [ { name: "storage", path: "/rails/storage" } ])) }
    assert_response :success
    assert_includes missing.calls.map(&:args), [ "volume", "create", "equip_storage" ]
    assert Project.find_by!(name: "equip").project_volumes.find_by!(name: "storage").placed_at

    # A HOLD (a required secret with no value) places nothing: the deploy
    # stops there, and the volumes can still be chosen.
    held = FakeDocker.new
    use_fake_docker(held) { sync(equip_payload(name: "held", volumes: [ { name: "data", path: "/data" } ])) }
    assert_response :unprocessable_entity
    assert_match "HOLD", json["error"]
    assert_empty held.calls
    assert_nil Project.find_by!(name: "held").project_volumes.find_by(name: "data")&.placed_at

    # Chosen elsewhere than where it is: the sync is refused, DNS untouched.
    nfs = storage_locations(:unas)
    other = Project.create!(name: "other", app_service: "app", services: %w[app], health: "/up", port: 80)
    other.project_volumes.create!(name: "media", location: nfs)
    plain = FakeDocker.new { |args| args[0..1] == %w[volume inspect] ? DockerCommand::Result.new(success: true, output: "null\n") : nil }
    use_fake_docker(plain) { sync(equip_payload(name: "other", variables: optional, volumes: [ { name: "media", path: "/media" } ])) }
    assert_response :unprocessable_entity
    assert_match "other_media already exists on local disk, not unas-nfs", json["error"]
    assert_not plain.calls.any? { |c| c.args[0..1] == %w[volume create] }
  end

  test "maintenance follows the domains" do
    Installation.current.update!(cloudflare_account_id: ACCOUNT, tunnel_id: TUNNEL, cloudflare_api_token: TOKEN)
    # The custom domains' zones aren't in Cloudflare: looked up, left alone.
    stub_request(:get, %r{\A#{API}/zones}).to_return(status: 200, headers: { "Content-Type" => "application/json" }, body: { success: true, errors: [], result: [] }.to_json)
    optional = [ { name: "RAILS_MASTER_KEY", required: false } ]
    sync(equip_payload(variables: optional, domains: [ "equipping.com" ]))
    Project.find_by!(name: "equip").update!(maintenance_since: Time.current)
    pushed = []
    stub_request(:put, "#{API}/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/configurations").to_return do |request|
      pushed << JSON.parse(request.body).dig("config", "ingress").filter_map { |r| r["hostname"] }
      { status: 200, headers: { "Content-Type" => "application/json" }, body: { success: true, errors: [], messages: [], result: {} }.to_json }
    end

    sync(equip_payload(variables: optional, domains: [ "www.equipping.com" ]))
    assert_includes pushed.last, "www.equipping.com"
    assert_not_includes pushed.last, "equipping.com"
  end

  test "sync records the maintenance page" do
    optional = [ { name: "RAILS_MASTER_KEY", required: false } ]
    sync(equip_payload(variables: optional, maintenance_page: "<h1>{{project}}</h1>"))
    assert_response :success
    project = Project.find_by!(name: "equip")
    assert_equal "<h1>{{project}}</h1>", project.maintenance_page

    # The largest page, all tags: JSON escapes each < and > to six bytes,
    # so the body is ~3 MB, and it still syncs.
    worst = "<>" * (512.kilobytes / 2)
    sync(equip_payload(variables: optional, maintenance_page: worst))
    assert_response :success
    assert_equal worst, project.reload.maintenance_page
    project.update!(maintenance_page: "<h1>{{project}}</h1>")

    [ "x" * (512.kilobytes + 1), 42 ].each do |page|
      sync(equip_payload(variables: optional, maintenance_page: page))
      assert_response :unprocessable_entity
      assert_includes json["errors"].keys, "maintenance_page"
    end
    assert_equal "<h1>{{project}}</h1>", project.reload.maintenance_page

    sync(equip_payload(variables: optional))
    assert_nil project.reload.maintenance_page, "no page in the file: the default again"
  end

  # What the project page shows beyond what Houston acts on (the facts strip,
  # the console box). An older CLI sends none.
  test "details: stored, validated, optional" do
    optional = [ { name: "RAILS_MASTER_KEY", required: false } ]
    details = { images: { db: "postgres:17" }, cpus: "1.5", memory: "2 GB", console: "bin/rails console" }
    sync(equip_payload(variables: optional, details:))
    assert_response :success
    project = Project.find_by!(name: "equip")
    assert_equal({ "images" => { "db" => "postgres:17" }, "cpus" => "1.5", "memory" => "2 GB", "console" => "bin/rails console" }, project.details)

    sync(equip_payload(variables: optional))
    assert_response :success
    assert_equal({}, project.reload.details, "an older CLI's sync leaves nothing stale behind")

    [ "x", { images: [ "postgres" ] }, { images: { web: "nginx" } }, { images: { db: "x" * 256 } }, { cpus: 2 },
      { memory: "x" * 33 }, { console: "x" * 501 }, { console: "a\u0000b" } ].each do |bad|
      sync(equip_payload(variables: optional, details: bad))
      assert_response :unprocessable_entity, bad.inspect
      assert_includes json["errors"].keys, "details", bad.inspect
    end
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

    sync(equip_payload(domains: Array.new(200_000) { |i| "d#{i}.example-domain.com" }))
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

  # Found on equip's first production deploy: the sync pointed
  # equip.svnmns.com at Houston, the deploy failed its release hook, and the
  # name answered 502 while Coolify's copy still ran. A project that has
  # never served keeps its names where they are until its first GO.
  test "a project's first deploy points its names only once it's GO" do
    record = { type: "CNAME", name: "equip.svnmns.com", content: "#{TUNNEL}.cfargotunnel.com", proxied: true, comment: "managed-by:houston project:equip" }
    Installation.current.update!(dns_mode: "per_host", cloudflare_zone_id: ZONE, tunnel_id: TUNNEL, cloudflare_api_token: TOKEN)

    sync(equip_payload(variables: [], domains: %w[equipping.com]))
    assert_response :success
    assert_equal "after_first_go", json["dns"]
    assert_equal({ "equipping.com" => "AFTER FIRST GO" }, json["domains"].transform_values { |v| v["state"] })
    assert_not_requested :any, /api\.cloudflare\.com/

    project = Project.find_by!(name: "equip")
    failed, token, = Deploy.start!(project, sha: "a" * 40, ref: "refs/heads/main")
    patch "/api/deploys/#{failed.id}", params: { status: "no_go", error: "release hook failed" }.to_json, headers: api_headers.merge("X-Houston-Deploy-Token" => token)
    assert_response :success
    assert_not_requested :any, /api\.cloudflare\.com/ # a NO-GO first deploy leaves the names alone

    cf(:get, "/zones/#{ZONE}/dns_records", query: { "name" => "equip.svnmns.com" }, result: [])
    create = cf(:post, "/zones/#{ZONE}/dns_records", result: { id: "rec1" }).with(body: record)
    cf(:get, "/zones", query: { "name" => "equipping.com" }, result: [ { id: "z2", name: "equipping.com", status: "active" } ])
    cf(:get, "/zones/z2/dns_records", query: { "name" => "equipping.com" }, result: [])
    apex = cf(:post, "/zones/z2/dns_records", result: { id: "r1" })
    deploy, token, = Deploy.start!(project, sha: "b" * 40, ref: "refs/heads/main")
    patch "/api/deploys/#{deploy.id}", params: { status: "go" }.to_json, headers: api_headers.merge("X-Houston-Deploy-Token" => token)
    assert_response :success
    assert_requested create
    assert_requested apex
    assert_equal "DNS OK", project.reload.domain_states.dig("equipping.com", "state")

    # A second GO doesn't point again: the sync does, on every deploy from now on.
    WebMock.reset!
    later, token, = Deploy.start!(project, sha: "c" * 40, ref: "refs/heads/main")
    patch "/api/deploys/#{later.id}", params: { status: "go" }.to_json, headers: api_headers.merge("X-Houston-Deploy-Token" => token)
    assert_response :success
    assert_not_requested :any, /api\.cloudflare\.com/
  end

  test "a Cloudflare failure at the first GO leaves the deploy GO; the next sync points" do
    Installation.current.update!(dns_mode: "per_host", cloudflare_zone_id: ZONE, tunnel_id: TUNNEL, cloudflare_api_token: TOKEN)
    sync(equip_payload(variables: []))
    project = Project.find_by!(name: "equip")
    stub_request(:any, /api\.cloudflare\.com/).to_return(status: 500, body: { success: false, errors: [ { code: 1, message: "boom" } ] }.to_json)
    deploy, token, = Deploy.start!(project, sha: "a" * 40, ref: "refs/heads/main")
    patch "/api/deploys/#{deploy.id}", params: { status: "go" }.to_json, headers: api_headers.merge("X-Houston-Deploy-Token" => token)
    assert_response :success
    assert_equal "go", deploy.reload.status

    WebMock.reset!
    cf(:get, "/zones/#{ZONE}/dns_records", query: { "name" => "equip.svnmns.com" }, result: [])
    create = cf(:post, "/zones/#{ZONE}/dns_records", result: { id: "rec1" })
    sync(equip_payload(variables: []))
    assert_response :success
    assert_equal "per_host", json["dns"]
    assert_requested create
  end

  test "sync points <name>.<base> in host-by-host mode" do
    record = { type: "CNAME", name: "equip.svnmns.com", content: "#{TUNNEL}.cfargotunnel.com", proxied: true, comment: "managed-by:houston project:equip" }

    Installation.current.update!(dns_mode: "per_host", cloudflare_zone_id: ZONE, tunnel_id: TUNNEL, cloudflare_api_token: TOKEN)
    # A project that has served: its names are pointed on every sync.
    make_deploy(make_project("equip", services: %w[app db]), 1, "go")
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
    make_deploy(make_project("equip", services: %w[app db]), 1, "go") # it has served, so the sync points
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

  # A restore's sync (docs/plans/restore.md, Batch 6 review): the snapshot's
  # compose.yml is checked and kept with the restore, which owns it (its
  # token), never stored on the project until the restore has switched.
  # Nothing is placed or pointed.
  test "a restore's sync only checks, and keeps the payload with the restore" do
    optional = [ { name: "RAILS_MASTER_KEY", required: false } ]
    sync(equip_payload(variables: optional, volumes: [ { name: "storage", path: "/rails/storage" } ]))
    project = Project.find_by!(name: "equip")
    before = project.reload.attributes.except("updated_at")
    restore, token = restore_in_flight(project)
    old = equip_payload(variables: optional, services: %w[app], volumes: [ { name: "media", path: "/media" } ], restore_deploy: restore.id)

    docker = FakeDocker.new { nil }
    use_fake_docker(docker) { sync(old, token:) }
    assert_response :success
    assert_equal({ "project" => "equip", "host" => "equip.svnmns.com", "dns" => "wildcard", "domains" => {}, "generation" => 1 }, json)
    assert_equal before, project.reload.attributes.except("updated_at")
    assert_empty docker.calls, "nothing placed"
    assert_equal [ { "name" => "media", "path" => "/media" } ], restore.reload.sync_payload["volumes"]
    assert_equal %w[app], restore.sync_payload["services"]
    assert_not restore.sync_payload.key?("restore_deploy")

    # A variable the snapshot's code requires, with no value: HOLD, still nothing stored on the project.
    sync(equip_payload(variables: [ { name: "OLD_KEY", required: true } ], restore_deploy: restore.id), token:)
    assert_response :unprocessable_entity
    assert_equal [ "OLD_KEY" ], json["missing"]
    assert_equal before, project.reload.attributes.except("updated_at")

    # Only the restore's owner, while it's in flight, for its own project.
    { "another token" => [ old, "not-its-token", :forbidden ],
      "another project" => [ old.merge(name: "nobody"), token, :unprocessable_entity ] }.each do |name, (payload, t, status)|
      sync(payload, token: t)
      assert_response status, name
    end
    ordinary, ordinary_token, = Deploy.start!(make_project("other"), sha: "c" * 40, ref: "refs/heads/main")
    sync(equip_payload(name: "other", variables: optional, restore_deploy: ordinary.id), token: ordinary_token)
    assert_response :unprocessable_entity
    restore.update!(status: "no_go", finished_at: Time.current)
    sync(old, token:)
    assert_response :conflict
    assert_equal before, project.reload.attributes.except("updated_at")
  end

  # What serves is the truth: a restore that switched traffic but never got
  # to say so is caught up by the next sync, deploy or restore. Which restore
  # it was is what kamal-proxy routes to: its generation and its commit.
  test "sync catches the generation up to the one serving" do
    optional = [ { name: "RAILS_MASTER_KEY", required: false } ]
    sync(equip_payload(variables: optional))
    project = Project.find_by!(name: "equip")
    a1, a2 = "a" * 40, "c" * 40
    switched = project.deploys.create!(number: 1, sha: a1, ref: "refs/restore/33333333", status: "no_go", kind: "restore", token_digest: "",
                                       heartbeat_at: Time.current, generation: 2, error: "abandoned",
                                       sync_payload: equip_payload(variables: optional, volumes: [ { name: "media", path: "/media" } ]).as_json)

    sync(equip_payload(variables: optional, serving_generation: 3, serving_sha: a1)) # no restore ever built 3
    assert_equal [ 1, 1 ], [ json["generation"], project.reload.data_generation ]
    sync(equip_payload(variables: optional, serving_generation: "2", serving_sha: a1))
    sync(equip_payload(variables: optional, serving_generation: 2, serving_sha: "b" * 40)) # not that restore's commit
    sync(equip_payload(variables: optional, serving_generation: 2))
    assert_equal 1, project.reload.data_generation

    # The operator asks again (a2) before anything caught up: a newer restore
    # of the same generation, not the one serving. Its check sync catches up
    # to the one kamal-proxy routes to, and applies that one's compose.yml.
    checking, token = restore_in_flight(project, generation: 2)
    checking.update!(sha: a2)
    # And one of the same commit, asked for again, still queued: not it either.
    project.deploys.create!(number: 3, sha: a1, ref: "refs/restore/33333333", status: "no_go", kind: "restore", token_digest: "",
                            heartbeat_at: Time.current, generation: 2, error: "abandoned")
    sync(equip_payload(variables: optional, serving_generation: 2, serving_sha: a1, restore_deploy: checking.id), token:)
    assert_equal [ 2, 2 ], [ json["generation"], project.reload.data_generation ]
    assert_equal [ { "name" => "media", "path" => "/media" } ], project.volumes
    assert switched.reload.switched_at
    assert_nil checking.reload.switched_at
    assert_equal switched.number, project.running_deploy.number
    checking.update!(status: "no_go", finished_at: Time.current)
    sync(equip_payload(variables: optional, serving_generation: 1)) # never backwards
    assert_equal [ 2, 2 ], [ json["generation"], project.reload.data_generation ]

    # A deploy's sync catches up too, even to a restore whose compose.yml
    # can't be applied (it's logged; the deploy's own replaces it), and its
    # volumes are placed in the caught-up generation.
    project.deploys.create!(number: 4, sha: "b" * 40, ref: "refs/restore/44444444", status: "no_go", kind: "restore", token_digest: "",
                            heartbeat_at: Time.current, generation: 3, error: "abandoned", sync_payload: { "name" => "equip" })
    placing = FakeDocker.new { |args| args[0..1] == %w[volume inspect] ? DockerCommand::Result.new(success: true, output: "null\n") : nil }
    use_fake_docker(placing) { sync(equip_payload(variables: optional, volumes: [ { name: "storage", path: "/rails/storage" } ], serving_generation: 3, serving_sha: "b" * 40)) }
    assert_response :success
    assert_equal [ 3, 3 ], [ json["generation"], project.reload.data_generation ]
    assert_includes placing.all_args, "equip.g3_storage"
    # Generation 2 was a restore's too, but the project is past it: never back.
    sync(equip_payload(variables: optional, serving_generation: 2, serving_sha: a1))
    assert_equal [ 3, 3 ], [ json["generation"], project.reload.data_generation ]
  end

  # A restore queued or in flight owns the project's config until it's done:
  # a hand houston deploy's sync would change what its snapshot and cleanup read.
  test "a sync waits for a restore" do
    optional = [ { name: "RAILS_MASTER_KEY", required: false } ]
    sync(equip_payload(variables: optional))
    project = Project.find_by!(name: "equip")
    restore, = restore_in_flight(project)
    sync(equip_payload(variables: optional, volumes: [ { name: "other", path: "/other" } ]))
    assert_response :conflict
    assert_match "restore ##{restore.number} is in flight", json["error"]
    assert_equal [], project.reload.volumes

    # One whose runner went silent doesn't: houston deploy by hand is how an
    # admin takes it over (Deploy.start! abandons it), and it syncs first.
    restore.update!(heartbeat_at: (Deploy::STALE_AFTER + 1.second).ago)
    sync(equip_payload(variables: optional))
    assert_response :success
  end
end
