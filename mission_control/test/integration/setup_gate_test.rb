require "test_helper"

class SetupGateTest < ActionDispatch::IntegrationTest
  setup { Session.delete_all; User.delete_all }

  test "every page leads to setup until an admin exists" do
    [ "/", "/sign-in" ].each do |path|
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

  test "the admin is taken to the Cloudflare step" do
    User.create!(email_address: "admin@example.com", password: "correct horse battery")
    Installation.delete_all
    post session_path, params: { email_address: "admin@example.com", password: "correct horse battery" }

    get root_path
    assert_redirected_to setup_cloudflare_path

    delete session_path
    assert_redirected_to sign_in_path
    get root_path
    assert_redirected_to sign_in_path
  end

  test "the admin is taken to the storage step" do
    StorageLocation.delete_all
    User.create!(email_address: "admin@example.com", password: "correct horse battery")
    post session_path, params: { email_address: "admin@example.com", password: "correct horse battery" }

    get root_path
    assert_redirected_to setup_storage_path
  end
end
