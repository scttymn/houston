require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/backup_helpers"
require_relative "../support/api_helpers"

class ApiV1BackupsTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers
  include ActiveJob::TestHelper

  setup do
    @token, = ApiToken.issue!("agent")
    @project = make_backup_project
  end

  def api(verb, path, token: @token)
    send(verb, "/api/v1#{path}", headers: { "Authorization" => "Bearer #{token}", "Content-Type" => "application/json" })
  end

  def json = response.parsed_body

  test "snapshots over the API" do
    listing = [ snapshot_json(id: "11111111", time: "2026-09-21T03:00:00Z"),
                snapshot_json(id: "33333333", time: "2026-09-22T12:31:00Z", kind: "deploy", reason: "deploy", deploy: 7, bytes: 5) ]
    use_fake_docker(FakeDocker.new { DockerCommand::Result.new(success: true, output: listing.to_json) }) { api :get, "/projects/equip/snapshots" }
    assert_response :success
    assert_equal [ { "id" => "33333333".ljust(64, "0"), "short_id" => "33333333", "time" => "2026-09-22T12:31:00Z", "kind" => "deploy",
                     "reason" => "deploy", "deploy" => 7, "sha" => "a" * 40, "bytes" => 5 } ], json["snapshots"].first(1)
    assert_equal %w[33333333 11111111], json["snapshots"].map { |s| s["short_id"] }

    Rails.cache.clear
    use_fake_docker(FakeDocker.new { failure("Fatal: unable to open repository at /repo: permission denied\n") }) { api :get, "/projects/equip/snapshots" }
    assert_response :bad_gateway
    assert_match "unable to open repository", json["error"]

    api :get, "/projects/nope/snapshots"
    assert_response :not_found

    ENV["HOUSTON_RUNNER_TOKEN"] = ApiHelpers::RUNNER_TOKEN
    api :get, "/projects/equip/snapshots", token: ApiHelpers::RUNNER_TOKEN
    assert_response :unauthorized

    StorageLocation.update_all(acknowledged_at: nil)
    api :get, "/projects/equip/snapshots"
    assert_response :conflict
    assert_match "no backup storage yet", json["error"]
  ensure
    ENV.delete("HOUSTON_RUNNER_TOKEN")
  end

  test "backing up over the API" do
    assert_enqueued_jobs(1, only: BackupJob) do
      api :post, "/projects/equip/backups"
      assert_response :accepted
      first = json
      assert_equal [ "queued", "auto", "manual" ], first.values_at("status", "kind", "reason")
      api :post, "/projects/equip/backups"
      assert_equal first["id"], json["id"], "a queued backup is returned as it is"
    end

    run = BackupRun.last
    run.update!(status: "go", snapshot_id: SNAPSHOT, bytes: 410_000_000, sha: "a" * 40, started_at: 1.minute.ago, finished_at: Time.current)
    api :get, "/projects/equip/backups/#{run.id}"
    assert_response :success
    assert_equal [ run.id, "go", SNAPSHOT, 410_000_000, "a" * 40, nil ], json.values_at("id", "status", "snapshot_id", "bytes", "sha", "deploy")
    assert json["finished_at"]
    api :get, "/projects/equip/backups/latest"
    assert_equal run.id, json["id"]

    # Silent for 2 minutes: reads as NO-GO, as the page shows it.
    run.update!(status: "running", heartbeat_at: 3.minutes.ago)
    api :get, "/projects/equip/backups/#{run.id}"
    assert_equal "no_go", json["status"]
    assert_match "Mission Control stopped during the backup", json["error"]

    other = make_project("other")
    make_deploy(other, 1, "go")
    other_run = other.backup_runs.create!(location: storage_locations(:unas), kind: "auto", reason: "manual", status: "go", heartbeat_at: Time.current)
    api :get, "/projects/equip/backups/#{other_run.id}"
    assert_response :not_found

    fresh = make_project("fresh")
    api :post, "/projects/fresh/backups"
    assert_response :unprocessable_entity
    assert_match "nothing deployed yet", json["error"]
    assert_equal 0, fresh.backup_runs.count
  end

  test "the project shows its last backup" do
    api :get, "/projects/equip"
    assert_nil json["last_backup"]
    assert json.key?("last_backup")

    @project.backup_runs.create!(location: storage_locations(:unas), kind: "auto", reason: "manual", status: "no_go", error: "restic backup failed", heartbeat_at: Time.current)
    api :get, "/projects/equip"
    assert_equal [ "no_go", "restic backup failed" ], json["last_backup"].values_at("status", "error")
  end
end
