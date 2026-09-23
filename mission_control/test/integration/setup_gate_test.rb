require "test_helper"

class SetupGateTest < ActionDispatch::IntegrationTest
  setup { Session.delete_all; User.delete_all }

  test "every page leads to setup until an admin exists" do
    [ "/", "/session/new" ].each do |path|
      get path
      assert_redirected_to setup_path, "#{path} should lead to setup"
    end
  end

  test "unknown paths stay 404 rather than a catch-all redirect" do
    get "/anything"
    assert_response :not_found
  end

  test "the health check and assets don't" do
    get "/up"
    assert_response :success

    get ActionController::Base.helpers.asset_path("application.css")
    assert_response :success
  end
end
