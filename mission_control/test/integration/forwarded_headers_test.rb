require "test_helper"

# What Rails reads about a request's client is true, whichever way it came
# (security audit M1, L1). In production Thruster, in front of Puma in the
# same container, appends the address that connected to it to
# X-Forwarded-For and passes everything else a client sent through; tests
# send what Thruster would.
class ForwardedHeadersTest < ActionDispatch::IntegrationTest
  CLOUDFLARED = "172.18.0.3"

  setup { Rails.cache.clear }
  teardown { Rails.cache.clear }

  def sign_in_attempt(headers)
    post session_path, params: { email_address: users(:one).email_address, password: "wrong" }, headers: headers
    follow_redirect!
    response.body[/Try again later\.|Try another email address or password\./]
  end

  def with_cloudflared_at(*addresses)
    original = ForwardedHeaders.tunnel_addresses
    ForwardedHeaders.tunnel_addresses = -> { addresses }
    yield
  ensure
    ForwardedHeaders.tunnel_addresses = original
  end

  test "a client's forwarding headers don't move it off hooks.<base>" do
    host! "hooks.svnmns.com"
    [ { "X-Forwarded-Host" => "admin.svnmns.com" }, { "Forwarded" => "host=admin.svnmns.com" } ].each do |headers|
      get "/sign-in", headers: headers
      assert_response :not_found, headers.inspect
      assert_empty response.body
    end
  end

  test "on the network, X-Forwarded-For can't choose the rate limit's key" do
    10.times { |i| assert_match "another", sign_in_attempt("X-Forwarded-For" => "6.6.6.#{i}, 192.168.0.5", "Client-Ip" => "7.7.7.#{i}") }
    assert_equal "Try again later.", sign_in_attempt("X-Forwarded-For" => "6.6.6.99, 192.168.0.5")
    assert_match "another", sign_in_attempt("X-Forwarded-For" => "192.168.0.6"), "another address has its own count"
  end

  test "through the tunnel, each visitor has its own count; Cf-Connecting-Ip counts only from cloudflared" do
    with_cloudflared_at(CLOUDFLARED) do
      visitor = ->(ip) { { "X-Forwarded-For" => CLOUDFLARED, "Cf-Ray" => "8f-MCI", "Cf-Connecting-Ip" => ip } }
      10.times { sign_in_attempt(visitor.("203.0.113.1")) }
      assert_equal "Try again later.", sign_in_attempt(visitor.("203.0.113.1"))
      assert_match "another", sign_in_attempt(visitor.("203.0.113.2"))

      10.times { |i| sign_in_attempt("X-Forwarded-For" => "192.168.0.5", "Cf-Ray" => "x", "Cf-Connecting-Ip" => "6.6.6.#{i}") }
      assert_equal "Try again later.", sign_in_attempt("X-Forwarded-For" => "192.168.0.5", "Cf-Ray" => "x", "Cf-Connecting-Ip" => "6.6.6.99")
    end
  end

  test "failed sign-ins have a cap across every address" do
    SessionsController::EVERYONE.times { |i| sign_in_attempt("X-Forwarded-For" => "10.0.#{i / 200}.#{i % 200}") }
    assert_equal "Try again later.", sign_in_attempt("X-Forwarded-For" => "10.9.9.9")
  end

  test "https counts only from the tunnel" do
    post session_path, params: { email_address: users(:one).email_address, password: "password" },
                       headers: { "X-Forwarded-For" => "192.168.0.5", "X-Forwarded-Proto" => "https" }
    assert_no_match(/secure/i, response.headers["Set-Cookie"].to_s, "a LAN request saying https is still http")

    with_cloudflared_at(CLOUDFLARED) do
      post session_path, params: { email_address: users(:one).email_address, password: "password" },
                         headers: { "X-Forwarded-For" => CLOUDFLARED, "Cf-Ray" => "8f-MCI", "Cf-Connecting-Ip" => "203.0.113.1", "X-Forwarded-Proto" => "https" }
      assert_match(/secure/i, response.headers["Set-Cookie"].to_s)
      assert_equal "203.0.113.1", Session.order(:id).last.ip_address
    end

    with_cloudflared_at do
      post session_path, params: { email_address: users(:one).email_address, password: "password" },
                         headers: { "X-Forwarded-For" => CLOUDFLARED, "Cf-Ray" => "8f-MCI", "Cf-Connecting-Ip" => "203.0.113.1", "X-Forwarded-Proto" => "https" }
      assert_match(/secure/i, response.headers["Set-Cookie"].to_s, "cloudflared not found: still https through the tunnel")
      assert_equal CLOUDFLARED, Session.order(:id).last.ip_address, "but the visitor's address isn't trusted"
    end
  end
end
