require "test_helper"
require_relative "../support/api_helpers"
require_relative "../support/project_helpers"
require_relative "../support/backup_helpers"

# A restore deploy asks Mission Control to restore its snapshot's data into
# the generation it's building: its owner only, while it's in flight.
class ApiRestoreDataTest < ActionDispatch::IntegrationTest
  include ApiHelpers
  include ProjectHelpers
  include BackupHelpers
  include ActiveJob::TestHelper

  setup do
    @project = make_backup_project
    @restore, @token, = Deploy.start!(@project, sha: "a" * 40, ref: "refs/heads/main")
    @restore.update!(kind: "restore", source_snapshot_id: SNAPSHOT, source_location: storage_locations(:unas), generation: 2)
  end

  def restore_data(verb, deploy: @restore, token: @token, **extra)
    send(verb, "/api/deploys/#{deploy.id}/restore_data", headers: api_headers(**extra).merge("X-Houston-Deploy-Token" => token))
  end

  test "a restore asks for its data" do
    assert_enqueued_with(job: BackupJob, queue: "snapshots") { restore_data :post }
    assert_response :accepted
    run = BackupRun.find(json["id"])
    assert_equal [ "restore", "queued", SNAPSHOT, @restore.number ], [ run.operation, run.status, run.source_snapshot_id, run.deploy_number ]
    assert_equal storage_locations(:unas), run.location

    assert_no_enqueued_jobs { restore_data :post }
    assert_equal run.id, json["id"], "a retry gets the same run"

    restore_data :get
    assert_equal [ run.id, "queued" ], json.values_at("id", "status")
  end

  test "who may ask, and when" do
    restore_data :post, token: "not-this-deploys"
    assert_response :forbidden
    restore_data :post, "Cf-Ray" => "8a1b2c3d4e5f-MCI"
    assert_response :not_found
    personal, = ApiToken.issue!("agent")
    post "/api/deploys/#{@restore.id}/restore_data", headers: { "Authorization" => "Bearer #{personal}", "X-Houston-Deploy-Token" => @token }
    assert_response :unauthorized

    @restore.update!(kind: "deploy")
    restore_data :post
    assert_response :unprocessable_entity
    @restore.update!(kind: "restore", status: "no_go", finished_at: Time.current)
    restore_data :post
    assert_response :conflict
    assert_equal 0, BackupRun.count
  end
end
