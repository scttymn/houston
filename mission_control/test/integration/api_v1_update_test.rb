require "test_helper"
require_relative "../support/fake_docker"

# houston update (CLI-first): the server's update over the remote API
# (docs/plans/update-from-mission-control.md, row 10).
class ApiV1UpdateTest < ActionDispatch::IntegrationTest
  include FakeDockerHelper

  LABELS = %({"com.docker.compose.project.config_files":"/opt/houston/compose.yml","com.docker.compose.project.working_dir":"/opt/houston"}).freeze

  setup do
    ENV["HOUSTON_VERSION"] = "v0.4.2"
    ENV["HOUSTON_RUNNER_IMAGE"] = "houston/runner:local"
    Installation.current.update!(latest_release: "v0.4.3")
    @token, = ApiToken.issue!("laptop")
  end

  teardown { %w[HOUSTON_VERSION HOUSTON_RUNNER_IMAGE].each { ENV.delete(it) } }

  def auth = { "Authorization" => "Bearer #{@token}", "Content-Type" => "application/json" }
  def json = response.parsed_body
  def docker = FakeDocker.new { |args| DockerCommand::Result.new(success: true, output: LABELS) if args.first == "inspect" }

  test "show, start, show" do
    get "/api/v1/update", headers: auth
    assert_response :success
    assert_equal({ "version" => "v0.4.2", "latest" => "v0.4.3", "update" => nil }, json)

    use_fake_docker(docker) { post "/api/v1/update", params: {}.to_json, headers: auth }
    assert_response :accepted
    id = ServerUpdate.sole.id
    assert_equal({ "id" => id, "to" => "v0.4.3", "from" => "v0.4.2", "status" => "running" }, json["update"].slice("id", "to", "from", "status"))
    assert_match(/Updating to v0\.4\.3/, json["message"])

    ServerUpdate.sole.update!(step: "Pulling Houston v0.4.3")
    get "/api/v1/update", headers: auth
    assert_equal "Pulling Houston v0.4.3", json["update"]["step"]

    ServerUpdate.sole.update!(status: "rolled_back", finished_at: Time.current, log: "404\n")
    get "/api/v1/update", headers: auth
    assert_equal [ id, "rolled_back", "404\n" ], json["update"].values_at("id", "status", "log")
    assert json["update"]["finished_at"]
  end

  test "a version, refused, and a bad request" do
    use_fake_docker(docker) { post "/api/v1/update", params: { version: "v0.4.1" }.to_json, headers: auth }
    assert_response :unprocessable_entity
    assert_match(/isn't newer/, json["error"])

    post "/api/v1/update", params: { version: 4 }.to_json, headers: auth
    assert_response :bad_request
    assert_equal 0, ServerUpdate.count
  end

  test "a token is needed" do
    use_fake_docker(docker) { |fake| post "/api/v1/update", params: {}.to_json, headers: { "Content-Type" => "application/json" }; @calls = fake.calls.size }
    assert_response :unauthorized
    assert_equal [ 0, 0 ], [ @calls, ServerUpdate.count ]
    get "/api/v1/update"
    assert_response :unauthorized
  end

  test "check asks GitHub now" do
    stub_request(:get, "https://api.github.com/repos/scttymn/houston/releases/latest")
      .to_return(body: { tag_name: "v0.4.5", html_url: "https://github.com/scttymn/houston/releases/tag/v0.4.5" }.to_json)
    post "/api/v1/update/check", headers: auth
    assert_response :success
    assert_equal [ "v0.4.2", "v0.4.5", "v0.4.5 is out." ], json.values_at("version", "latest", "message")

    stub_request(:get, "https://api.github.com/repos/scttymn/houston/releases/latest").to_timeout
    post "/api/v1/update/check", headers: auth
    assert_response :bad_gateway
    assert_match(/Couldn't reach GitHub/, json["error"])

    post "/api/v1/update/check"
    assert_response :unauthorized
  end

  test "me says when an update is running" do
    get "/api/v1/me", headers: auth
    assert_nil json["updating"]
    ServerUpdate.create!(to_version: "v0.4.3", from_version: "v0.4.2", status: "running", started_at: Time.current)
    get "/api/v1/me", headers: auth
    assert_equal "v0.4.3", json["updating"]
  end
end
