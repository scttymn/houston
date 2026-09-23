require "test_helper"

class SignInTest < ActionDispatch::IntegrationTest
  setup { @admin = users(:one) }

  test "pages need a signed-in admin" do
    get root_path
    assert_redirected_to new_session_path

    post session_path, params: { email_address: @admin.email_address, password: "wrong" }
    assert_redirected_to new_session_path
    get root_path
    assert_redirected_to new_session_path

    post session_path, params: { email_address: @admin.email_address, password: "password" }
    assert_redirected_to root_path
    get root_path
    assert_response :success

    delete session_path
    get root_path
    assert_redirected_to new_session_path
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
end
