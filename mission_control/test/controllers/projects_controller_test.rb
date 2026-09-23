require "test_helper"
require_relative "../support/cloudflare_stubs"

class ProjectsControllerTest < ActionDispatch::IntegrationTest
  include CloudflareStubs

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
    assert_select ".path [aria-disabled=true]", /Add project/
    assert_select ".path a", { text: /Add project/, count: 0 }
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

  test "on admin.<base> the route row is GO without probing" do
    stub_tunnel
    sign_in_on_admin_host

    get root_path
    assert_response :success

    assert_select ".preflight li[data-check=admin-route][data-state=go]", /You're using it now/
    assert_not_requested :get, "https://admin.svnmns.com/ping"
  end
end
