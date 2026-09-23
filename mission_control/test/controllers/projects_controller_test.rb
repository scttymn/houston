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
end
