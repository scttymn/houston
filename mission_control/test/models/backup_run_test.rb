require "test_helper"
require_relative "../support/project_helpers"

class BackupRunTest < ActiveSupport::TestCase
  include ProjectHelpers
  include ActiveJob::TestHelper

  setup do
    @project = make_project("equip")
    make_deploy(@project, 1, "go")
  end

  test "requesting a backup" do
    run = nil
    assert_enqueued_with(job: BackupJob, queue: "backups") { run = BackupRun.request!(@project) }
    assert_equal [ "queued", "auto", "manual" ], [ run.status, run.kind, run.reason ]
    assert_equal storage_locations(:unas), run.location

    # A double click queues one.
    assert_no_enqueued_jobs { assert_equal run, BackupRun.request!(@project) }
    assert_equal 1, BackupRun.count

    fresh = make_project("fresh")
    error = assert_raises(BackupRun::Refused) { BackupRun.request!(fresh) }
    assert_match "nothing deployed yet", error.message

    StorageLocation.update_all(acknowledged_at: nil)
    run.update!(status: "go")
    error = assert_raises(BackupRun::Refused) { BackupRun.request!(@project) }
    assert_match "no backup storage", error.message
    assert_equal 1, BackupRun.count
  end

  test "the database allows one running backup and one queued manual backup per project" do
    other = make_project("other")
    row = ->(project, status, reason = "manual") { { project_id: project.id, location_id: storage_locations(:unas).id, kind: "auto", reason:, status:, token_digest: "", heartbeat_at: Time.current } }

    BackupRun.insert!(row.(@project, "running"))
    assert_raises(ActiveRecord::RecordNotUnique) { BackupRun.insert!(row.(@project, "running")) }
    BackupRun.insert!(row.(@project, "queued"))
    assert_raises(ActiveRecord::RecordNotUnique) { BackupRun.insert!(row.(@project, "queued")) }
    BackupRun.insert!(row.(@project, "queued", "schedule"))
    BackupRun.insert!(row.(other, "running"))
  end

  test "claiming a run" do
    first = BackupRun.request!(@project)
    token = BackupRun.claim!(first)
    assert token
    assert_equal "running", first.reload.status
    assert_equal BackupRun.digest(token), first.token_digest
    assert first.started_at

    # Claimed already: a second claim of the same run gets nothing.
    assert_nil BackupRun.claim!(first)

    # A live running backup holds the next one back.
    second = BackupRun.request!(@project)
    assert_equal :busy, BackupRun.claim!(second)
    assert_equal "queued", second.reload.status

    # Silent for 2 minutes: abandoned, and the next one claims.
    first.update_columns(heartbeat_at: 3.minutes.ago)
    token2 = BackupRun.claim!(second)
    assert_kind_of String, token2
    assert_equal [ "no_go", "Mission Control stopped during the backup (no word since #{first.heartbeat_at.utc.iso8601})" ], [ first.reload.status, first.error ]
    assert first.finished_at
    assert_equal "running", second.reload.status
  end

  test "one scheduled backup per project per day" do
    row = ->(status, date) { { project_id: @project.id, location_id: storage_locations(:unas).id, kind: "auto", reason: "schedule", status:, scheduled_for: date, token_digest: "", heartbeat_at: Time.current } }
    BackupRun.insert!(row.("go", Date.new(2026, 9, 22)))
    assert_raises(ActiveRecord::RecordNotUnique) { BackupRun.insert!(row.("queued", Date.new(2026, 9, 22))) }
    BackupRun.insert!(row.("queued", Date.new(2026, 9, 23)))

    # request! with a date already taken returns that day's run, queueing nothing.
    taken = BackupRun.find_by!(scheduled_for: Date.new(2026, 9, 22))
    assert_no_enqueued_jobs { assert_equal taken, BackupRun.request!(@project, reason: "schedule", scheduled_for: Date.new(2026, 9, 22)) }
  end

  test "one snapshot per deploy" do
    row = ->(number) { { project_id: @project.id, location_id: storage_locations(:unas).id, kind: "deploy", reason: "deploy", status: "queued", deploy_number: number, token_digest: "", heartbeat_at: Time.current } }
    BackupRun.insert!(row.(7))
    assert_raises(ActiveRecord::RecordNotUnique) { BackupRun.insert!(row.(7).merge(status: "go")) }
    BackupRun.insert!(row.(8))
  end

  test "backups wait while a restore is in flight, except its own" do
    @project.deploys.create!(number: 2, sha: "c" * 40, ref: "refs/restore/cccccccc", kind: "restore", status: "in_flight", token_digest: "d", heartbeat_at: Time.current, generation: 2)
    manual = BackupRun.request!(@project)
    assert_equal :busy, BackupRun.claim!(manual)
    safety = @project.backup_runs.create!(location: storage_locations(:unas), kind: "deploy", reason: "restore", deploy_number: 2, status: "queued", heartbeat_at: Time.current)
    assert_kind_of String, BackupRun.claim!(safety)
  end
end
