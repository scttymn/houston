require "test_helper"

class PingsControllerTest < ActionDispatch::IntegrationTest
  test "ping names this install, signed out" do
    get "/ping"

    assert_response :success
    assert_equal "text/plain", response.media_type
    assert_equal Installation.identity, response.body
    assert_match(/\A\h{32}\z/, response.body)
    assert_not_includes response.body, Rails.application.secret_key_base.first(16)
    assert_nil response.headers["Set-Cookie"]
  end

  test "ping answers before setup instead of redirecting" do
    Session.delete_all
    User.delete_all

    get "/ping"

    assert_response :success
    assert_equal Installation.identity, response.body
  end
end
