require "test_helper"
require_relative "../support/project_helpers"

class ApiV1SecretsTest < ActionDispatch::IntegrationTest
  include ProjectHelpers

  setup do
    @token, = ApiToken.issue!("agent")
    @project = make_project("garage", variables: [ { "name" => "RAILS_MASTER_KEY", "required" => true }, { "name" => "SENTRY_DSN", "required" => false } ])
    @project.secrets.create!(key: "RAILS_MASTER_KEY", value: "the-master-key-value")
  end

  def api(verb, path, body = nil)
    send(verb, "/api/v1/projects/garage/secrets#{path}", params: body&.to_json,
         headers: { "Authorization" => "Bearer #{@token}", "Content-Type" => "application/json" })
  end

  def json = response.parsed_body

  test "listing" do
    api :get, ""
    assert_response :success
    assert_equal [ [ "RAILS_MASTER_KEY", true, true ], [ "SENTRY_DSN", false, false ] ], json["secrets"].map { |s| s.values_at("name", "required", "set") }
    assert_not_includes response.body, "the-master-key-value"
  end

  test "setting" do
    api :put, "/SENTRY_DSN", { value: "https://sentry.example/1" }
    assert_response :success
    assert_equal({ "name" => "SENTRY_DSN", "set" => true }, json)
    assert_not_includes response.body, "sentry.example"
    assert_equal "https://sentry.example/1", @project.secrets.find_by!(key: "SENTRY_DSN").value

    api :put, "/SENTRY_DSN", { value: 'C:\\data' }
    assert_response :unprocessable_entity
    assert_match(/base64/i, json["error"])
    api :put, "/SENTRY_DSN", { value: "" }
    assert_response :unprocessable_entity
    assert_equal "https://sentry.example/1", @project.secrets.find_by!(key: "SENTRY_DSN").reload.value

    api :put, "/NOT_IN_THE_FILE", { value: "x" }
    assert_response :not_found
    assert_nil @project.secrets.find_by(key: "NOT_IN_THE_FILE")
  end

  test "unsetting and generating" do
    api :delete, "/RAILS_MASTER_KEY"
    assert_response :success
    assert_nil @project.secrets.find_by(key: "RAILS_MASTER_KEY")

    api :post, "/SENTRY_DSN/generate"
    assert_response :success
    value = @project.secrets.find_by!(key: "SENTRY_DSN").value
    # Long enough for any framework's key (Phoenix and Rails want 64 bytes).
    assert_operator value.bytesize, :>=, 64
    assert_match(/\A[A-Za-z0-9_-]+\z/, value)
    assert_not_includes response.body, value
  end
end
