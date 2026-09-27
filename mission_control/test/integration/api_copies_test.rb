require "test_helper"
require_relative "../support/api_helpers"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/backup_helpers"
require_relative "../support/handover_helpers"

# A copy's deploy, as the runner drives it (docs/plans/copy-project.md,
# Batch 4): its job says what to leave for the handover, Mission Control
# copies the old project's data into the new one, moves the hosts, and
# settles the copy when the deploy finishes.
class ApiCopiesTest < ActionDispatch::IntegrationTest
  include ApiHelpers
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers
  include ActiveJob::TestHelper
  include HandoverHelpers

  setup do
    @old = make_backup_project # equip: storage volume, Postgres (db), deploy #1 GO
    @old.update!(domains: %w[equipping.com], chosen_backup_location: storage_locations(:unas))
    @new = make_project("equip-go", services: %w[app db cache], domains: %w[equipping.com equip.svnmns.com])
    @new.update!(volumes: @old.volumes, databases: @old.databases)
    @deploy = @new.deploys.create!(number: 1, sha: "c" * 40, ref: "refs/heads/main", status: "queued", kind: "copy", token_digest: "", heartbeat_at: Time.current,
                                   sync_payload: { "name" => "equip-go", "volumes" => @old.volumes })
    @copy = ProjectCopy.create!(from_project: @old, project: @new, deploy: @deploy, from: "equip", to: "equip-go", sha: "c" * 40, by: "admin")
  end

  def claim
    post "/api/runner/jobs/claim", params: { runner: "houston-runner-1", wait: 0 }.to_json, headers: api_headers
    assert_response :success
    [ json.dig("deploy", "token"), json ]
  end

  def runner(verb, path, token) = send(verb, "/api/deploys/#{@deploy.id}/#{path}", headers: api_headers.merge("X-Houston-Deploy-Token" => token))

  test "the copy's job, and its claim marks the copy running" do
    _, job = claim
    assert_equal "copy", job.dig("deploy", "kind")
    assert_equal({ "from" => "equip", "placeholder" => "equip-go.houston-copy.invalid", "exclude_hosts" => %w[equipping.com equip.svnmns.com] }, job.dig("deploy", "copy"))
    assert_equal "running", @copy.reload.status
  end

  test "copy data: a late snapshot of the old project, then restored into the new one" do
    token, = claim
    assert_enqueued_with(job: BackupJob) { runner(:post, "copy_data", token) }
    assert_response :accepted
    snap = @copy.reload.snapshot_run
    assert_equal [ @old, "deploy", "copy", "queued", storage_locations(:unas) ], [ snap.project, snap.kind, snap.reason, snap.status, snap.location ]
    assert_equal snap.id, json["id"]

    runner(:post, "copy_data", token)
    assert_equal snap, @copy.reload.snapshot_run, "a retried POST gets the same run"

    runner(:get, "copy_data", token)
    assert_equal "queued", json["status"]

    snap.update!(status: "go", snapshot_id: "5c5edd4c" + "0" * 56, sha: "a" * 40)
    assert_enqueued_with(job: BackupJob) { runner(:get, "copy_data", token) }
    restore = @new.backup_runs.sole
    assert_equal [ "restore", "restore", 1, "5c5edd4c" + "0" * 56, storage_locations(:unas) ],
                 [ restore.operation, restore.reason, restore.deploy_number, restore.source_snapshot_id, restore.location ]
    assert_equal [ restore.id, "queued" ], json.values_at("id", "status")

    restore.update!(status: "go")
    runner(:get, "copy_data", token)
    assert_equal "go", json["status"]
  end

  test "copy data: nothing to copy, or the snapshot failing" do
    token, = claim
    @old.update!(volumes: [], databases: [])
    runner(:post, "copy_data", token)
    assert_equal [ "skipped", "equip has no data to copy" ], json.values_at("status", "error")

    @old.update!(volumes: [ { "name" => "storage", "path" => "/rails/storage" } ])
    @copy.update!(snapshot_run: @old.backup_runs.create!(location: storage_locations(:unas), kind: "deploy", reason: "copy", status: "no_go", error: "pg_dump failed", heartbeat_at: Time.current))
    runner(:get, "copy_data", token)
    assert_equal "no_go", json["status"]
    assert_match "the snapshot of equip failed: pg_dump failed", json["error"]
  end

  test "the handover, asked for by the copy's runner" do
    token, = claim
    calls = 0
    with_handover(handover_doing(forward: -> { calls += 1; %w[equipping.com] })) { runner(:post, "handover", token) }
    assert_response :success
    assert_equal [ { "handed_over" => %w[equipping.com] }, 1 ], [ json, calls ]

    with_handover(handover_doing(forward: -> { raise Handover::Failed, "kamal-proxy didn't move the hosts: conflict" })) { runner(:post, "handover", token) }
    assert_response :unprocessable_entity
    assert_match "conflict", json["error"]

    runner(:post, "handover", "nope")
    assert_response :forbidden
    @deploy.update!(kind: "deploy")
    runner(:post, "handover", token)
    assert_response :unprocessable_entity
  end

  test "a copy deploy's end settles the copy" do
    token, = claim
    @new.update!(domains: []) # its first GO points only its own names: none in Cloudflare here
    patch "/api/deploys/#{@deploy.id}", params: { status: "go", log: "GO\n" }.to_json, headers: api_headers.merge("X-Houston-Deploy-Token" => token)
    assert_response :success
    assert_equal "go", @copy.reload.status
  end

  test "a copy that fails before its handover takes the new project away; after it, never" do
    token, = claim
    @deploy.update_columns(log: "Build…\n")
    assert_enqueued_with(job: CopyCleanupJob) do
      patch "/api/deploys/#{@deploy.id}", params: { status: "no_go", error: "kamal deploy failed" }.to_json, headers: api_headers.merge("X-Houston-Deploy-Token" => token)
    end
    @copy.reload
    assert_equal [ "no_go", "kamal deploy failed" ], [ @copy.status, @copy.error ]
    assert_equal "Build…\n", @copy.log, "the deploy's log outlives the new project"
    perform_enqueued_jobs(only: CopyCleanupJob)
    deletion = @new.deletions.sole
    assert_equal [ true, "Houston (the copy of equip failed)" ], [ deletion.delete_backups, deletion.by ]
    assert @old.reload.persisted?

    ProjectDeletion.delete_all
    @copy.update!(status: "running", handed_over: %w[equipping.com], handed_over_at: Time.current)
    @deploy.update!(status: "in_flight")
    assert_no_enqueued_jobs(only: CopyCleanupJob) do
      patch "/api/deploys/#{@deploy.id}", params: { status: "no_go", error: "stopped" }.to_json, headers: api_headers.merge("X-Houston-Deploy-Token" => token)
    end
    assert_equal 0, ProjectDeletion.count
    assert_match "it serves the hosts it took", @copy.reload.error
  end
end
