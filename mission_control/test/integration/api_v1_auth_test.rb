require "test_helper"
require_relative "../support/api_helpers"

class ApiV1AuthTest < ActionDispatch::IntegrationTest
  include ApiHelpers

  setup { @token, @record = ApiToken.issue!("laptop") }

  def me(token = @token, headers: {}) = get("/api/v1/me", headers: { "Authorization" => "Bearer #{token}" }.merge(headers))

  test "a personal token reaches the remote API" do
    travel_to(Time.utc(2026, 9, 23, 12)) do
      me(headers: { "Cf-Ray" => "8a1b2c3d-MCI", "Cf-Connecting-Ip" => "203.0.113.9" })
      assert_response :success
      assert_equal({ "token" => "laptop", "server" => "svnmns.com", "version" => HoustonVersion.current, "latest" => nil, "updating" => nil }, json)
    end
    assert_equal Time.utc(2026, 9, 23, 12), @record.reload.last_used_at

    travel_to(Time.utc(2026, 9, 23, 12, 0, 30)) { me }
    assert_equal Time.utc(2026, 9, 23, 12), @record.reload.last_used_at, "not rewritten within a minute"
    travel_to(Time.utc(2026, 9, 23, 12, 2)) { me }
    assert_equal Time.utc(2026, 9, 23, 12, 2), @record.reload.last_used_at
  end

  test "me says when a newer release is out" do
    ENV["HOUSTON_VERSION"] = "v0.1.0"
    Installation.current.update!(latest_release: "v0.1.1", latest_release_url: "https://github.com/scttymn/houston/releases/tag/v0.1.1")
    me
    assert_equal [ "v0.1.0", "v0.1.1" ], json.values_at("version", "latest")
  ensure
    ENV.delete("HOUSTON_VERSION")
  end

  test "the remote API takes only personal tokens" do
    get "/api/v1/me"
    assert_response :unauthorized
    [ "hou_wrong", RUNNER_TOKEN, "" ].each do |token|
      me(token)
      assert_response :unauthorized, token.inspect
    end
    @record.destroy!
    me
    assert_response :unauthorized

    token, = ApiToken.issue!("other")
    Installation.current.update!(cloudflare_connected_at: nil)
    me(token)
    assert_response :conflict
  end

  test "personal tokens can't use the runner API" do
    headers = api_headers(token: @token)
    post "/api/projects/sync", params: equip_payload.to_json, headers: headers
    assert_response :unauthorized
    get "/api/projects/equip/secrets/RAILS_MASTER_KEY", headers: headers
    assert_response :unauthorized
    post "/api/runner/jobs/claim", params: { runner: "houston-runner-1", wait: 0 }.to_json, headers: headers
    assert_response :unauthorized
  end
end
