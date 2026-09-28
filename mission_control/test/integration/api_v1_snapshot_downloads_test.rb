require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/backup_helpers"
require_relative "../support/api_helpers"

# Downloading a snapshot over the API (docs/plans/download-snapshot.md, Batch 2).
class ApiV1SnapshotDownloadsTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers
  include ApiHelpers

  setup do
    @token, = ApiToken.issue!("agent")
    make_backup_project
  end

  def download(path = "/api/v1/projects/equip/snapshots/5c5edd4c/download", token: @token)
    get path, headers: { "Authorization" => "Bearer #{token}" }
  end

  def docker(download: [ "PK\x03\x04abc".b, "def" ], created: nil)
    FakeDocker.new(download:) do |args, _env|
      if args.include?("snapshots")
        DockerCommand::Result.new(success: true, output: [ snapshot_json(id: "5c5edd4c", time: "2026-09-21T03:00:00Z") ].to_json)
      elsif args.first == "inspect"
        created ? DockerCommand::Result.new(success: true, output: "c0ffee #{created.utc.iso8601} someone-else\n") : failure("Error: No such object\n")
      end
    end
  end

  def logged
    io = StringIO.new
    original = Rails.logger
    Rails.logger = ActiveSupport::Logger.new(io)
    yield
    io.string
  ensure
    Rails.logger = original
  end

  test "downloading a snapshot over the API" do
    fake = docker
    log = logged { use_fake_docker(fake) { download } }

    assert_response :success
    assert_equal "application/zip", response.media_type
    assert_match(/\Aattachment; filename="equip-20260921-0300Z-5c5edd4c.zip"/, response.headers["Content-Disposition"])
    assert_includes response.headers["Cache-Control"], "no-store"
    assert_equal "PK\x03\x04abcdef".b, response.body.b
    assert_includes fake.calls.find { |c| c.args.include?("dump") }.args, "#{storage_locations(:unas).volume_name}:/repo", "the project's backup location by default"
    assert_includes log, "download: token agent equip 5c5edd4c from unas-nfs: started"

    use_fake_docker(docker) { download "/api/v1/projects/equip/snapshots/5c5edd4c/download?location=unas-nfs" }
    assert_response :success
  end

  test "a download over the API that's refused" do
    fake = docker
    [ "hou_wrong", ApiHelpers::RUNNER_TOKEN ].each do |token|
      use_fake_docker(fake) { download(token:) }
      assert_response :unauthorized, token
    end
    assert_empty fake.calls

    { "/api/v1/projects/nope/snapshots/5c5edd4c/download" => /no project nope/,
      "/api/v1/projects/equip/snapshots/99999999/download" => /snapshot 99999999 isn't in unas-nfs's snapshots of equip/,
      "/api/v1/projects/equip/snapshots/5c5edd4c/download?location=nowhere" => /equip never backed up to nowhere/ }.each do |path, error|
      fake = docker
      use_fake_docker(fake) { download path }
      assert_response :not_found, path
      assert_match error, response.parsed_body["error"]
      assert fake.calls.none? { |c| c.args.include?("dump") }, path
    end

    in_use = failure("docker: Error response from daemon: Conflict. The container name \"/houston-export\" is already in use\n", code: 125)
    use_fake_docker(docker(download: in_use, created: 2.minutes.ago)) { download }
    assert_response :conflict
    assert_match(/another download started at .* is still running/, response.parsed_body["error"])

    use_fake_docker(docker(download: failure("Fatal: wrong password or no key found\n", code: 12))) { download }
    assert_response :bad_gateway
    assert_match(/exit 12.*wrong password/, response.parsed_body["error"])

    StorageLocation.update_all(acknowledged_at: nil)
    use_fake_docker(docker) { download }
    assert_response :conflict
    assert_match(/no backup storage yet/, response.parsed_body["error"])
  end
end
