require "test_helper"

class SignInTest < ActionDispatch::IntegrationTest
  setup { @admin = users(:one) }

  test "pages need a signed-in admin" do
    get root_path
    assert_redirected_to sign_in_path

    post session_path, params: { email_address: @admin.email_address, password: "wrong" }
    assert_redirected_to sign_in_path
    get root_path
    assert_redirected_to sign_in_path

    post session_path, params: { email_address: @admin.email_address, password: "password" }
    assert_redirected_to root_path
    get root_path
    assert_response :success

    delete session_path
    get root_path
    assert_redirected_to sign_in_path
  end

  test "signing in is at /sign-in" do
    get "/sign-in"
    assert_response :success
    assert_select "form[action='/session'][method=post]", 1

    get "/session/new"
    assert_redirected_to "/sign-in", "old bookmarks still work"
    assert_response :moved_permanently
  end

  test "password reset is gone" do
    get "/passwords/new"
    assert_response :not_found
  end

  test "only the session routes that exist are routed" do
    get "/session/edit"
    assert_response :not_found
    get "/session"
    assert_response :not_found
  end

  test "the session cookie is secure over https" do
    post session_path, params: { email_address: @admin.email_address, password: "password" }
    assert_no_match(/;\s*secure/i, session_cookie_header, "plain http (LAN first run) must still work")

    delete session_path
    # https reaches Mission Control only through Cloudflare, which adds Cf-Ray.
    https!
    post session_path, params: { email_address: @admin.email_address, password: "password" }, headers: { "Cf-Ray" => "8f-MCI", "X-Forwarded-Proto" => "https" }
    assert_match(/;\s*secure/i, session_cookie_header)
  end

  private
    def session_cookie_header
      Array(response.headers["Set-Cookie"]).join("\n").lines.find { |l| l.start_with?("session_id=") }.to_s
    end
end
