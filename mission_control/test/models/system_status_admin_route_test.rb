require "test_helper"

class SystemStatusAdminRouteTest < ActiveSupport::TestCase
  PING = "https://admin.svnmns.com/ping"

  setup do
    WebMock.reset!
    @installation = Installation.current
    @installation.update!(cloudflare_connected_at: Time.current)
  end

  test "the route to admin.<base>, by what came back and how long it's been" do
    [
      [ { body: Installation.identity }, 1.minute, :go, nil ],
      [ { body: "OK" }, 1.minute, :hold, /answered 200, not from this Mission Control/ ],
      [ { status: 404, body: "404 page not found" }, 1.minute, :hold, /answered 404, not from this Mission Control/ ],
      [ { status: 530, body: "error code: 1033" }, 1.minute, :hold, /530 from Cloudflare/ ],
      [ :timeout, 1.minute, :hold, /no answer in 2 s/ ],
      [ { status: 404, body: "404 page not found" }, 11.minutes, :nogo, /answered 404, not from this Mission Control/ ],
      [ :timeout, 11.minutes, :nogo, /no answer in 2 s/ ]
    ].each do |response, after, state, reason|
      Rails.cache.clear
      WebMock.reset!
      stub = stub_request(:get, PING)
      response == :timeout ? stub.to_timeout : stub.to_return(**response)

      travel after do
        route = SystemStatus.admin_route(@installation)
        assert_equal state, route.state, [ response, after ].inspect
        reason ? assert_match(reason, route.reason) : assert_nil(route.reason)
      end
    end
  end

  test "an unresolvable name says so" do
    stub_request(:get, PING).to_raise(SocketError.new("getaddrinfo: Name or service not known"))

    assert_match(/can't look up admin\.svnmns\.com from this server/, SystemStatus.admin_route(@installation).reason)
  end

  test "nothing is probed before Cloudflare is connected" do
    @installation.update!(cloudflare_connected_at: nil)

    assert_nil SystemStatus.admin_route(@installation)
    assert_not_requested :get, PING
  end

  test "on admin.<base> itself the route is GO without a probe" do
    route = SystemStatus.admin_route(@installation, on_admin: true)

    assert_equal :go, route.state
    assert_not_requested :get, PING
  end

  test "a match is cached for 30 s, no match for 5 s" do
    ping = stub_request(:get, PING).to_return(body: Installation.identity)
    2.times { SystemStatus.admin_route(@installation) }
    assert_requested ping, times: 1
    travel(31.seconds) { SystemStatus.admin_route(@installation) }
    assert_requested ping, times: 2

    Rails.cache.clear
    WebMock.reset!
    ping = stub_request(:get, PING).to_return(status: 404)
    2.times { SystemStatus.admin_route(@installation) }
    assert_requested ping, times: 1
    travel(6.seconds) { SystemStatus.admin_route(@installation) }
    assert_requested ping, times: 2
  end
end
