require "test_helper"
require_relative "../support/fake_docker"

# houston port / port open / port close (CLI-first): Settings › Port 3000
# over the remote API.
class ApiV1PortTest < ActionDispatch::IntegrationTest
  include FakeDockerHelper

  LABELS = %({"com.docker.compose.project.config_files":"/opt/houston/compose.yml","com.docker.compose.project.working_dir":"/opt/houston"}).freeze

  setup do
    ENV["HOUSTON_RUNNER_IMAGE"] = "houston/runner:local"
    Rails.cache.clear
    @token, = ApiToken.issue!("laptop")
  end

  teardown do
    ENV.delete("HOUSTON_RUNNER_IMAGE")
    Rails.cache.clear
  end

  def auth = { "Authorization" => "Bearer #{@token}" }
  def json = response.parsed_body
  def docker = FakeDocker.new { |args| DockerCommand::Result.new(success: true, output: args.include?("{{json .HostConfig.PortBindings}}") ? %({"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"3000"}]}) : LABELS) if args.first == "inspect" }

  test "show, close, open" do
    use_fake_docker(docker) { get "/api/v1/port", headers: auth }
    assert_response :success
    assert_equal({ "open" => true, "address" => "0.0.0.0", "saved" => "open" }, json)

    use_fake_docker(docker) { put "/api/v1/port", params: { open: false }.to_json, headers: auth.merge("Content-Type" => "application/json") }
    assert_response :accepted
    assert_equal "closed", json["saved"]
    assert_match(/restarts for a few seconds/, json["message"])
    assert_not Installation.current.reload.port_open
  end

  test "refused, and a bad request" do
    ENV.delete("HOUSTON_RUNNER_IMAGE")
    use_fake_docker(docker) { put "/api/v1/port", params: { open: false }.to_json, headers: auth.merge("Content-Type" => "application/json") }
    assert_response :unprocessable_entity
    assert_match(/can't/i, json["error"])

    put "/api/v1/port", params: { open: "maybe" }.to_json, headers: auth.merge("Content-Type" => "application/json")
    assert_response :bad_request
    assert Installation.current.reload.port_open
  end

  test "a token is needed" do
    put "/api/v1/port", params: { open: false }.to_json, headers: { "Content-Type" => "application/json" }
    assert_response :unauthorized
    assert Installation.current.reload.port_open
  end
end
