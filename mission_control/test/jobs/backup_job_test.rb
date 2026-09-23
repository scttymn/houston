require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/backup_helpers"

class BackupJobTest < ActiveJob::TestCase
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers

  setup { @project = make_backup_project }

  test "a backup job runs the backup" do
    run = BackupRun.request!(@project)
    use_fake_docker(backup_docker) { |fake| BackupJob.perform_now(run); @fake = fake }

    assert_equal [ "go", SNAPSHOT, 410_000_000 ], [ run.reload.status, run.snapshot_id, run.bytes ]
    assert_equal 1, restic_backups(@fake).size
  end

  test "a second delivery does nothing" do
    run = BackupRun.request!(@project)
    use_fake_docker(backup_docker) do |fake|
      BackupJob.perform_now(run)
      BackupJob.perform_now(run)
      assert_equal 1, restic_backups(fake).size, "restic backup ran more than once"
    end
    assert_equal "go", run.reload.status
  end

  test "a job for a project that's already backing up waits and tries again" do
    running = BackupRun.request!(@project)
    BackupRun.claim!(running)
    run = BackupRun.create!(project: @project, location: running.location, kind: "auto", reason: "schedule", status: "queued", token_digest: "", heartbeat_at: Time.current)

    use_fake_docker(backup_docker) do |fake|
      assert_enqueued_with(job: BackupJob, args: [ run ]) { BackupJob.perform_now(run) }
      assert_empty fake.calls
    end
    assert_equal "queued", run.reload.status

    # Still busy when the waiting runs out: NO-GO, never queued forever.
    job = BackupJob.new(run)
    job.exception_executions = { [ BackupJob::Busy ].to_s => 1_000 } # retry_on counts per exception
    use_fake_docker(backup_docker) { job.perform_now }
    assert_equal "no_go", run.reload.status
    assert_match "for the project's running backup", run.error
  end

  # The run is abandoned and taken over while this job is still working
  # (its heartbeat stalled, say): its finish must not overwrite anything.
  test "a job that lost its run finalizes nothing" do
    run = BackupRun.request!(@project)
    takeover = nil
    fake = backup_docker(->(args) { args.include?(StorageLocation::RESTIC_IMAGE) && args.include?("backup") } => -> {
      run.reload.update_columns(heartbeat_at: 3.minutes.ago)
      takeover = BackupRun.create!(project: @project, location: run.location, kind: "auto", reason: "schedule", status: "queued", token_digest: "", heartbeat_at: Time.current)
      BackupRun.claim!(takeover)
      DockerCommand::Result.new(success: true, output: summary_line)
    })
    use_fake_docker(fake) { BackupJob.perform_now(run) }

    run.reload
    assert_equal "no_go", run.status
    assert_match "Mission Control stopped during the backup", run.error
    assert_nil run.snapshot_id
    assert_equal "running", takeover.reload.status
  end
end
