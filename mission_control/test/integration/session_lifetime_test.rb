require "test_helper"

# Sessions end (security audit L2), and one made on the network, over plain
# HTTP, doesn't work through the tunnel, nor the other way round (H3).
# Cloudflare adds Cf-Ray to everything it forwards, and a client can't
# remove it.
class SessionLifetimeTest < ActionDispatch::IntegrationTest
  TUNNEL = { "Cf-Ray" => "8f-MCI", "Cf-Connecting-Ip" => "203.0.113.1" }.freeze

  setup { Installation.current.update!(cloudflare_account_id: "acc", tunnel_id: "tun") }

  def sign_in(headers = {})
    post session_path, params: { email_address: users(:one).email_address, password: "password" }, headers: headers
    assert_response :redirect
    assert_no_match "sign-in", response.location, "signed in"
  end

  def signed_in?(headers = {})
    get settings_path, headers: headers
    response.successful?
  end

  test "a session made on the network doesn't work through the tunnel" do
    sign_in
    assert signed_in?
    assert_not signed_in?(TUNNEL), "a LAN cookie replayed through the tunnel"
    assert signed_in?, "refusing it there doesn't end it here"
  end

  test "a session made through the tunnel doesn't work on the network" do
    sign_in(TUNNEL)
    assert signed_in?(TUNNEL)
    assert_not signed_in?
  end

  test "a session ends after two weeks unused, and a month after sign-in" do
    sign_in
    travel Session::IDLE - 1.day
    assert signed_in?, "used within two weeks"
    travel Session::IDLE - 1.day
    assert signed_in?, "each use keeps it going"

    sign_in
    idle = Session.order(:id).last
    travel Session::IDLE + 1.hour
    assert_not signed_in?, "two weeks unused"
    assert_not Session.exists?(idle.id), "an ended session is gone"

    # A cookie from before sessions ended (cookies.signed.permanent: 20 years).
    sign_in_as users(:one)
    7.times { travel 4.days; assert signed_in? }
    travel 3.days
    assert_not signed_in?, "a month after sign-in, even in use"
  end

  test "the cookie lasts as long as the session can" do
    sign_in
    expires = response.headers["Set-Cookie"].to_s[/expires=([^;]+)/i, 1]
    assert expires, "the cookie has an expiry"
    assert_in_delta Session::LIFETIME.from_now, Time.httpdate(expires), 1.minute
  end
end
