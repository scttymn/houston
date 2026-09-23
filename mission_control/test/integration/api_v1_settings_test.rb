require "test_helper"

class ApiV1SettingsTest < ActionDispatch::IntegrationTest
  setup { @token, = ApiToken.issue!("agent") }

  def api(verb, path, body = nil)
    send(verb, "/api/v1#{path}", params: body&.to_json, headers: { "Authorization" => "Bearer #{@token}", "Content-Type" => "application/json" })
  end

  def json = response.parsed_body

  test "settings over the API" do
    api :get, "/settings"
    assert_equal({ "base_domain" => "svnmns.com", "time_zone" => "UTC" }, json)

    api :patch, "/settings", { time_zone: "America/New_York" }
    assert_response :success
    assert_equal "America/New_York", json["time_zone"]
    assert_equal "America/New_York", Installation.current.time_zone

    api :patch, "/settings", { time_zone: "Mars/Olympus" }
    assert_response :unprocessable_entity
    assert_match "Mars/Olympus", json["error"]
    assert_equal "America/New_York", Installation.current.reload.time_zone
  end
end
