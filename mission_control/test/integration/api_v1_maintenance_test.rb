require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/tunnel_helpers"
require_relative "../support/api_helpers"

class ApiV1MaintenanceTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include TunnelHelpers

  setup do
    connect_tunnel
    @token, = ApiToken.issue!("agent")
    make_project("equip")
  end

  def api(verb, path, body = nil, token: @token)
    send(verb, "/api/v1#{path}", params: body&.to_json, headers: { "Authorization" => "Bearer #{token}", "Content-Type" => "application/json" })
  end

  def json = response.parsed_body

  test "maintenance over the API" do
    pushes = record_pushes
    api :put, "/projects/equip/maintenance", { on: true, message: "Back soon" }
    assert_response :success
    assert_equal [ true, "token agent", "Back soon" ], json.values_at("on", "by", "message")
    assert json["since"]

    api :get, "/projects/equip"
    assert_equal true, json.dig("maintenance", "on")

    api :put, "/projects/equip/maintenance", { on: false }
    assert_equal false, json["on"]
    assert_equal 2, pushes.size

    api :put, "/projects/nope/maintenance", { on: true }
    assert_response :not_found

    ENV["HOUSTON_RUNNER_TOKEN"] = ApiHelpers::RUNNER_TOKEN
    api :put, "/projects/equip/maintenance", { on: true }, token: ApiHelpers::RUNNER_TOKEN
    assert_response :unauthorized
  ensure
    ENV.delete("HOUSTON_RUNNER_TOKEN")
  end

  test "Cloudflare refusing is a 502 with its words" do
    record_pushes(status: 400, message: "Tunnel configuration is invalid")
    api :put, "/projects/equip/maintenance", { on: true }
    assert_response :bad_gateway
    assert_match "Tunnel configuration is invalid", json["error"]
  end
end
