require "test_helper"
require_relative "../support/api_helpers"

class ApiAuthTest < ActionDispatch::IntegrationTest
  include ApiHelpers

  test "the API needs the runner token" do
    sync(headers: { "Content-Type" => "application/json" })
    assert_response :unauthorized

    sync(headers: api_headers(token: "wrong"))
    assert_response :unauthorized

    ENV["HOUSTON_RUNNER_TOKEN"] = ""
    sync(headers: api_headers(token: ""))
    assert_response :unauthorized
    get "/api/projects/equip/secrets/RAILS_MASTER_KEY", headers: { "Authorization" => "Bearer " }
    assert_response :unauthorized

    assert_equal 0, Project.count
  end

  test "the API never answers through the tunnel" do
    project = Project.create!(name: "equip", app_service: "app", services: %w[app], health: "/up", port: 80,
                              variables: [ { "name" => "RAILS_MASTER_KEY", "required" => true } ])
    project.secrets.create!(key: "RAILS_MASTER_KEY", value: "s3cret")

    [ { "Cf-Ray" => "8a1b2c3d4e5f-MCI" }, { "Cf-Connecting-Ip" => "203.0.113.9" } ].each do |cloudflare|
      sync(equip_payload(name: "other"), headers: api_headers(**cloudflare))
      assert_response :not_found, cloudflare.inspect

      get "/api/projects/equip/secrets/RAILS_MASTER_KEY", headers: api_headers(**cloudflare)
      assert_response :not_found, cloudflare.inspect
      assert_not_includes response.body, "s3cret"
    end
    assert_equal [ "equip" ], Project.pluck(:name)
  end

  test "the API waits for setup" do
    Installation.current.update!(cloudflare_connected_at: nil)

    sync
    assert_response :conflict
    assert_match(/finish setup/i, json["error"])
    assert_equal 0, Project.count
  end
end
