require "test_helper"
require_relative "../support/cloudflare_stubs"
require_relative "../support/project_helpers"

class ProjectsControllerTest < ActionDispatch::IntegrationTest
  include CloudflareStubs
  include ProjectHelpers

  setup do
    Installation.current.update!(cloudflare_account_id: ACCOUNT, tunnel_id: TUNNEL, cloudflare_api_token: TOKEN)
    sign_in_as users(:one)
    @registry = stub_request(:get, "http://registry:5000/v2/").to_return(status: 200, body: "{}")
  end

  def stub_tunnel(connections: 4, status: 200)
    cf(:get, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}", status:,
       result: status == 200 ? { id: TUNNEL, name: "houston-svnmns", connections: Array.new(connections) { |i| { id: "c#{i}" } } } : nil,
       errors: status == 200 ? [] : [ { code: 1000, message: "Authentication error" } ])
  end

  # sign_in_as's cookie belongs to the default test host; on admin.<base>,
  # sign in through the form so the cookie is set for that host.
  def sign_in_on_admin_host
    host! "admin.svnmns.com"
    post session_path, params: { email_address: users(:one).email_address, password: "password" }
  end

  test "the empty flight board" do
    stub_tunnel

    get root_path

    assert_response :success
    assert_select ".eyebrow", /FLIGHT BOARD · SVNMNS\.COM/
    assert_select ".stat", 4
    assert_select ".stat", /PROJECTS\s*0/
    assert_select ".stat", /NEXT BACKUP\s*—/
    assert_select "h2", /Nothing on the pad yet/i
    assert_select ".path", /x-houston/i
    assert_select ".path a[href='/link']", /Add project/
    assert_select ".path pre", /houston init/
  end

  test "the banner points LAN visitors at admin.<base>" do
    stub_tunnel

    get root_path
    assert_select ".notice--go", /admin\.svnmns\.com/

    sign_in_on_admin_host
    get root_path
    assert_response :success
    assert_select ".notice--go", { text: /bookmark/, count: 0 }
  end

  test "the pre-flight tunnel row" do
    [
      [ { connections: 4 }, "GO", /4 connections to Cloudflare/ ],
      [ { connections: 0 }, "HOLD", /cloudflared isn't connected yet/ ],
      [ { status: 403 }, "?", /Can't check.*Authentication error/ ]
    ].each do |options, state, text|
      Rails.cache.clear
      WebMock.reset!
      stub_local_services
      stub_tunnel(**options)

      get root_path

      assert_select ".preflight li", /houston-svnmns/ do |rows|
        row = rows.find { |r| r.text.include?("houston-svnmns") }
        assert_includes row.text, state, options.inspect
        assert_match text, row.text, options.inspect
      end
    end
  end

  test "the header shows tunnel and registry status" do
    stub_tunnel(connections: 2)
    get root_path
    assert_select ".status", /TUNNEL\s*GO/
    assert_select ".status", /REGISTRY\s*GO/
    assert_select ".status", { text: /RUNNERS/, count: 0 }

    Rails.cache.clear
    WebMock.reset!
    stub_local_services
    stub_tunnel(connections: 0)
    stub_request(:get, "http://registry:5000/v2/").to_timeout
    get root_path
    assert_select ".status", /TUNNEL\s*NO-GO/
    assert_select ".status", /REGISTRY\s*NO-GO/
  end

  test "status checks are cached" do
    tunnel = stub_tunnel

    2.times { get root_path }

    assert_requested tunnel, times: 1
    assert_requested @registry, times: 1
  end

  test "the pre-flight DNS row says how the domain is shared" do
    stub_tunnel
    get root_path
    assert_select ".preflight li", /\*\.svnmns\.com.*New apps get a subdomain automatically/

    Installation.current.update!(dns_mode: "per_host")
    get root_path
    assert_select ".preflight li", /admin\.svnmns\.com and hooks\.svnmns\.com.*another server has \*\.svnmns\.com/
  end

  test "the banner and pre-flight row follow the route to admin.<base>" do
    stub_tunnel
    ping = "https://admin.svnmns.com/ping"

    get root_path
    assert_select ".notice--go a[href='https://admin.svnmns.com']"
    assert_select "[data-controller~=refresh]", 0
    assert_select ".preflight li[data-check=admin-route][data-state=go]", /Route to admin\.svnmns\.com/

    Rails.cache.clear
    stub_request(:get, ping).to_return(status: 404, body: "404 page not found")
    get root_path
    assert_select ".notice--hold", /switching admin\.svnmns\.com over/
    assert_select "a[href='https://admin.svnmns.com']", 0
    assert_select ".notice--hold[data-controller~=refresh]"
    assert_select ".preflight li[data-check=admin-route][data-state=hold]", /answered 404/

    Rails.cache.clear
    travel 11.minutes do
      get root_path
      assert_select ".notice--nogo", /isn't reaching Houston.*answered 404/m
      assert_select "a[href='https://admin.svnmns.com']", 0
      assert_select "[data-controller~=refresh]", 0
      assert_select ".preflight li[data-check=admin-route][data-state=nogo]"
    end
  end

  test "the header counts runners" do
    stub_tunnel
    Runner.create!(name: "houston-runner-1", last_seen_at: 10.seconds.ago)
    Runner.create!(name: "houston-runner-2", last_seen_at: 5.minutes.ago)
    ENV["HOUSTON_RUNNERS"] = "2"
    get root_path
    assert_select ".status", /RUNNERS\s*1\/2/
  ensure
    ENV.delete("HOUSTON_RUNNERS")
  end

  test "the pre-flight checks the route to hooks.<base>" do
    stub_tunnel
    get root_path
    assert_select ".preflight li[data-check=hooks-route][data-state=go]", /Route to hooks\.svnmns\.com/
  end

  test "on admin.<base> the route row is GO without probing" do
    stub_tunnel
    sign_in_on_admin_host

    get root_path
    assert_response :success

    assert_select ".preflight li[data-check=admin-route][data-state=go]", /You're using it now/
    assert_not_requested :get, "https://admin.svnmns.com/ping"
  end

  test "the flight board lists projects by their latest deploy" do
    stub_tunnel
    equip = make_project("equip", services: %w[app db], domains: %w[equipping.com])
    make_deploy(equip, 1, "go", sha: "a" * 40)
    make_deploy(equip, 2, "go", sha: "b" * 40)
    ride = make_project("rideclub")
    make_deploy(ride, 1, "go", sha: "c" * 40)
    make_deploy(ride, 2, "in_flight", sha: "d" * 40, step: "Build")
    valley = make_project("valley")
    make_deploy(valley, 1, "go", sha: "e" * 40)
    make_deploy(valley, 2, "no_go", sha: "f" * 40, error: "release hook failed (exit 3); the old version keeps serving")
    make_project("fresh")

    get root_path

    assert_response :success
    assert_select ".stat", /PROJECTS\s*4/
    assert_select ".stat", /IN FLIGHT\s*1/
    assert_select ".stat", /NO-GO\s*1/
    { "equip" => [ "GO", "bbbbbbb" ], "rideclub" => [ "IN FLIGHT", "ccccccc" ], "valley" => [ "NO-GO", "eeeeeee" ], "fresh" => [ "STANDBY", "—" ] }.each do |name, (state, sha)|
      assert_select "[data-project=#{name}]", 1 do |row|
        assert_match state, row.text, name
        assert_match sha, row.text, name
      end
    end
    assert_select "[data-project=equip]", /db/
    assert_select "[data-project=equip] a[href='https://equip.svnmns.com']"
    assert_select "[data-project=equip] a[href='https://equipping.com']"
    assert_select "[data-project=valley]", /release hook failed/
    assert_select "[data-project=equip] a[href='/projects/equip']"
    assert_select "h2", { text: /Nothing on the pad yet/i, count: 0 }
  end

  # The design's Main: pills, services with images, host rows, and LAST BACKUP.
  # docs/plans/live-flight-board.md: the board redraws itself on changes.
  test "the flight board shows the version" do
    stub_tunnel
    ENV["HOUSTON_VERSION"] = "v0.1.0"
    get root_path
    assert_select ".board__title > .eyebrow", "FLIGHT BOARD · SVNMNS.COM · V0.1.0"
  ensure
    ENV.delete("HOUSTON_VERSION")
  end

  test "the flight board says when an update is out" do
    stub_tunnel
    ENV["HOUSTON_VERSION"] = "v0.1.0"
    Installation.current.update!(latest_release: "v0.1.1", latest_release_url: "https://github.com/scttymn/houston/releases/tag/v0.1.1")
    get root_path
    assert_select ".update-notice" do
      assert_select ".notice__text", /v0\.1\.1 is out\. This server runs v0\.1\.0\./
      assert_select "a[href='https://github.com/scttymn/houston/releases/tag/v0.1.1']", /release notes/i
      assert_select "[data-clipboard-target=source]", "curl -fsSL https://github.com/scttymn/houston/releases/latest/download/install.sh | sudo HOUSTON_VERSION=v0.1.1 sh"
      assert_select "button[data-action='clipboard#copy']", "Copy"
    end

    ENV["HOUSTON_VERSION"] = "v0.1.1"
    get root_path
    assert_select ".update-notice", 0, "current"
  ensure
    ENV.delete("HOUSTON_VERSION")
  end

  # docs/plans/update-from-mission-control.md, row 9.
  test "the flight board updates the server" do
    travel_to Time.utc(2026, 9, 25, 22, 54)
    stub_tunnel
    ENV["HOUSTON_VERSION"] = "v0.4.2"
    Installation.current.update!(latest_release: "v0.4.3", latest_release_url: "https://github.com/scttymn/houston/releases/tag/v0.4.3")
    get root_path
    assert_select ".update-notice" do
      assert_select "form[action='#{server_update_path}'][method=post]" do
        assert_select "input[name=version][value='v0.4.3']", 1
        assert_select "button[data-turbo-confirm*='Mission Control restarts']", "Update to v0.4.3"
      end
      assert_select "[data-clipboard-target=source]", /HOUSTON_VERSION=v0\.4\.3/, "the command stays for SSH"
    end

    update = ServerUpdate.create!(to_version: "v0.4.3", from_version: "v0.4.2", status: "running", started_at: Time.zone.parse("2026-09-25 22:51:00 UTC"))
    get root_path
    assert_select ".update-notice form", 0, "no second update while one runs"
    assert_select ".server-update.notice--hold .notice__text", /Updating to v0\.4\.3 since 22:51 UTC\. Mission Control restarts on the way; deploys and backups wait\./
    assert_select ".server-update", { text: /taking long/, count: 0 }
    update.update!(started_at: 25.minutes.ago)
    get root_path
    assert_select ".server-update .notice__text", /taking long.*docker logs houston-update/m

    ENV["HOUSTON_VERSION"] = "v0.4.3"
    update.update!(status: "go", finished_at: 1.minute.ago, log: "==> Starting Houston\n")
    get root_path
    assert_select ".server-update.notice--go .notice__text", /Updated to v0\.4\.3\./
    assert_select ".update-notice", 0

    ENV["HOUSTON_VERSION"] = "v0.4.2"
    update.update!(status: "rolled_back", log: "curl: (22) The requested URL returned error: 404\n")
    get root_path
    assert_select ".server-update.notice--nogo" do
      assert_select ".notice__text", /The update to v0\.4\.3 failed, so this server went back to v0\.4\.2\./
      assert_select "pre", /returned error: 404/
    end
    update.update!(status: "no_go")
    get root_path
    assert_select ".server-update.notice--nogo .notice__text", /The update to v0\.4\.3 failed\./

    update.update!(finished_at: 25.hours.ago)
    get root_path
    assert_select ".server-update", 0, "a day later, it's gone"
  ensure
    ENV.delete("HOUSTON_VERSION")
  end

  test "the flight board listens for changes" do
    stub_tunnel
    make_deploy(make_project("equip"), 1, "go")
    get root_path
    assert_select "turbo-cable-stream-source[signed-stream-name]", 1
    assert_select "meta[name=turbo-refresh-method][content=morph]"
    assert_select "meta[name=turbo-refresh-scroll][content=preserve]"
  end

  test "the flight board follows the design" do
    stub_tunnel
    equip = make_project("equip", services: %w[app db], domains: %w[equipping.com])
    equip.update!(details: { "images" => { "db" => "postgres:17" } })
    make_deploy(equip, 1, "go")
    BackupRun.create!(project: equip, location: storage_locations(:unas), kind: "auto", reason: "schedule", status: "go",
                      heartbeat_at: Time.current, finished_at: Time.zone.parse("2026-09-24 03:00"), bytes: 412 * 1024 * 1024, snapshot_id: "a" * 64)
    ride = make_project("rideclub")
    make_deploy(ride, 1, "in_flight", step: "release hook")

    get root_path
    assert_select ".flight-head", /STATUS.*PROJECT.*RUNNING SHA.*DOMAINS.*LAST DEPLOY.*LAST BACKUP/m
    assert_select "[data-project=equip] .state-pill", "GO"
    assert_select "[data-project=equip] .flight__services", "db · postgres:17"
    assert_select "[data-project=equip] a.flight__host[href='https://equipping.com'] .flight__dot"
    assert_select "[data-project=equip] .flight__backup", /24 Sep 03:00.*auto · 412 MB/m
    assert_select "[data-project=rideclub].flight__row--in-flight"
    assert_select "[data-project=rideclub] .flight__backup", /—/
    assert_select ".board__title a.button[href='/link'] svg"
    assert_select "p.board__foot", /let the first\s+houston deploy\s+register it/
  end

  test "the top bar: status in the design's order, and Projects current on project pages" do
    stub_tunnel
    Runner.create!(name: "houston-runner-1", last_seen_at: 10.seconds.ago)
    Runner.create!(name: "houston-runner-2", last_seen_at: 10.seconds.ago)
    ENV["HOUSTON_RUNNERS"] = "2"
    get root_path
    assert_match(/TUNNEL.*RUNNERS 2\/2\s*GO.*REGISTRY/m, css_select(".status").first.text)

    make_project("equip")
    [ project_path("equip"), link_path ].each do |path|
      get path
      assert_select ".topbar__nav a.is-current", "Projects"
    end
  ensure
    ENV.delete("HOUSTON_RUNNERS")
  end

  test "the next backup" do
    stub_tunnel
    Installation.current.update!(time_zone: "Europe/Berlin")
    equip = make_project("equip")
    equip.update!(volumes: [ { "name" => "storage", "path" => "/rails/storage" } ], backup_schedule: "daily 03:00")
    make_deploy(equip, 1, "go")
    later = make_project("later")
    later.update!(databases: [ { "service" => "db", "image" => "postgres:17" } ], backup_schedule: "daily 22:15")
    make_deploy(later, 1, "go")

    travel_to Time.utc(2026, 9, 23, 12, 0) do # 14:00 in Berlin; equip ran today
      equip.backup_runs.create!(location: storage_locations(:unas), kind: "auto", reason: "schedule", status: "no_go", error: "restic backup failed",
                                scheduled_for: Date.new(2026, 9, 23), heartbeat_at: Time.current)
      get root_path
    end
    assert_select ".stat", /NEXT BACKUP\s*22:15 CEST/
    assert_select "[data-project='equip']", /backup NO-GO/
    assert_select "[data-project='later']" do |row|
      assert_no_match(/backup NO-GO/, row.text)
    end
  end

  test "a project in maintenance" do
    stub_tunnel
    make_project("equip").update!(maintenance_since: 10.minutes.ago, maintenance_by: "admin@example.com")
    make_project("other")
    get root_path
    assert_select "[data-project='equip']", /MAINTENANCE/
    assert_select "[data-project='other']" do |row|
      assert_no_match(/MAINTENANCE/, row.text)
    end
  end
end
