require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"

class ApiV1LogsTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeDockerHelper

  setup do
    @token, = ApiToken.issue!("agent")
    make_project("garage")
  end

  def logs(query = "", token: @token)
    get "/api/v1/projects/garage/logs#{query}", headers: { "Authorization" => "Bearer #{token}" }
  end

  def docker(running: "garage-web-abc123\n")
    FakeDocker.new(stream: [ "2026-09-23T12:00:00Z Started GET /up\n", "2026-09-23T12:00:01Z Completed 200\n" ]) do |args, _|
      DockerCommand::Result.new(success: true, output: running) if args.first == "ps"
    end
  end

  test "app logs stream" do
    use_fake_docker(docker) do |fake|
      logs
      assert_response :success
      assert_equal "text/plain", response.media_type
      assert_equal "2026-09-23T12:00:00Z Started GET /up\n2026-09-23T12:00:01Z Completed 200\n", response.body
      ps = fake.calls.find { |c| c.args.first == "ps" }.args
      assert_includes ps, "label=service=garage"
      assert_includes ps, "label=role=web"
      assert_equal [ "logs", "--timestamps", "--tail", "200", "garage-web-abc123" ], fake.streams.last

      logs "?tail=50&follow=1"
      assert_equal [ "logs", "--timestamps", "--tail", "50", "--follow", "garage-web-abc123" ], fake.streams.last
    end
  end

  test "logs are guarded" do
    use_fake_docker(docker(running: "")) do
      logs
      assert_response :not_found
      assert_match(/isn't running/, response.body)
    end
    use_fake_docker(docker) do |fake|
      %w[?tail=0 ?tail=10001 ?tail=x].each do |q|
        logs q
        assert_response :unprocessable_entity, q
      end
      get "/api/v1/projects/nope/logs", headers: { "Authorization" => "Bearer #{@token}" }
      assert_response :not_found
      logs token: "hou_wrong"
      assert_response :unauthorized
      assert_empty fake.streams
    end
  end
end
