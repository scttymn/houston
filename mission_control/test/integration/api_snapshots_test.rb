require "test_helper"
require_relative "../support/api_helpers"
require_relative "../support/project_helpers"
require_relative "../support/backup_helpers"

# The runner's pre-deploy snapshot: asked for by the deploy's owner, one per
# deploy, on its own queue.
class ApiSnapshotsTest < ActionDispatch::IntegrationTest
  include ApiHelpers
  include ProjectHelpers
  include BackupHelpers
  include ActiveJob::TestHelper

  setup do
    @project = make_backup_project # #1 is GO and serving
    @deploy, @token, = Deploy.start!(@project, sha: "b" * 40, ref: "refs/heads/main")
  end

  def snapshot(verb, deploy: @deploy, token: @token, **extra)
    send(verb, "/api/deploys/#{deploy.id}/snapshot", headers: api_headers(**extra).merge("X-Houston-Deploy-Token" => token))
  end

  test "a deploy asks for its snapshot" do
    assert_enqueued_with(job: BackupJob, queue: "snapshots") do
      snapshot :post
    end
    assert_response :accepted
    run = BackupRun.find(json["id"])
    assert_equal [ "queued", "deploy", "deploy", 2 ], [ run.status, run.kind, run.reason, run.deploy_number ]

    assert_no_enqueued_jobs { snapshot :post }
    assert_equal run.id, json["id"], "a retried request gets the same run"

    snapshot :get
    assert_response :success
    assert_equal [ run.id, "queued" ], json.values_at("id", "status")
    run.update!(status: "go", snapshot_id: SNAPSHOT, bytes: 410_000_000, sha: "a" * 40)
    snapshot :get
    assert_equal [ "go", SNAPSHOT, 410_000_000, "a" * 40 ], json.values_at("status", "snapshot_id", "bytes", "sha")

    # Asked again after it's done (a runner retrying): still that run.
    assert_no_enqueued_jobs { snapshot :post }
    assert_equal [ run.id, "go" ], json.values_at("id", "status")
  end

  test "who may ask, and when" do
    snapshot :post, token: "not-this-deploys"
    assert_response :forbidden

    snapshot :post, "Cf-Ray" => "8a1b2c3d4e5f-MCI"
    assert_response :not_found

    personal, = ApiToken.issue!("agent")
    post "/api/deploys/#{@deploy.id}/snapshot", headers: { "Authorization" => "Bearer #{personal}", "X-Houston-Deploy-Token" => @token }
    assert_response :unauthorized

    StorageLocation.update_all(acknowledged_at: nil)
    snapshot :post
    assert_response :conflict
    assert_match "no backup storage yet", json["error"]
    StorageLocation.update_all(acknowledged_at: Time.current)

    @deploy.update!(status: "no_go", finished_at: Time.current)
    snapshot :post
    assert_response :conflict
    assert_equal 0, BackupRun.count

    # A first deploy: nothing to snapshot yet.
    fresh = make_project("fresh")
    fresh.update!(volumes: [ { "name" => "data", "path" => "/data" } ])
    first, token, = Deploy.start!(fresh, sha: "c" * 40, ref: "refs/heads/main")
    snapshot :post, deploy: first, token: token
    assert_response :success
    assert_equal({ "status" => "skipped", "error" => "nothing deployed yet" }, json)
    assert_equal 0, BackupRun.count
  end
end
