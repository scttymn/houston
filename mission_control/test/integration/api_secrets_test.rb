require "test_helper"
require_relative "../support/api_helpers"

class ApiSecretsTest < ActionDispatch::IntegrationTest
  include ApiHelpers

  test "the runner reads a secret" do
    project = Project.create!(name: "equip", app_service: "app", services: %w[app], health: "/up", port: 80,
                              variables: [ { "name" => "RAILS_MASTER_KEY", "required" => true }, { "name" => "SENTRY_DSN", "required" => false } ])
    project.secrets.create!(key: "RAILS_MASTER_KEY", value: "k3y $HOME 'q' café")
    project.secrets.create!(key: "LEFTOVER", value: "from an older compose.yml")

    get "/api/projects/equip/secrets/RAILS_MASTER_KEY", headers: api_headers
    assert_response :success
    assert_equal "text/plain", response.media_type
    assert_equal "k3y $HOME 'q' café", response.body

    [ "/api/projects/nope/secrets/RAILS_MASTER_KEY",
      "/api/projects/equip/secrets/LEFTOVER",
      "/api/projects/equip/secrets/SENTRY_DSN" ].each do |path|
      get path, headers: api_headers
      assert_response :not_found, path
      assert_not_includes response.body, "older"
    end
  end
end
