require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_git"

# Asking to delete a project, and nothing new starting for it meanwhile
# (docs/plans/delete-project.md, Batch 2).
class ProjectDeletionTest < ActiveSupport::TestCase
  include ProjectHelpers
  include FakeGitHelper
  include ActiveJob::TestHelper

  setup do
    @project = make_linked_project("equip")
    make_deploy(@project, 1, "go")
  end

  def ask(confirm: "equip", delete_backups: false) = ProjectDeletion.request!(@project, confirm:, delete_backups:, by: "admin@example.com")

  def refused(message, **options)
    before = ProjectDeletion.count
    error = nil
    assert_no_enqueued_jobs(only: DeleteProjectJob) { error = assert_raises(ProjectDeletion::Refused) { ask(**options) } }
    assert_match message, error.message
    assert_equal before, ProjectDeletion.count, message
  end

  test "asking to delete" do
    deletion = nil
    assert_enqueued_with(job: DeleteProjectJob) { deletion = ask(delete_backups: true) }
    assert_equal [ "queued", "equip", "git@forgejo:houston/equip.git", "admin@example.com", true, @project ],
                 [ deletion.status, deletion.name, deletion.repo_url, deletion.by, deletion.delete_backups, deletion.project ]
    assert @project.deleting?
    assert_equal deletion, @project.deletion
  end

  test "asking to delete, refused" do
    refused("type equip to confirm", confirm: "Equip")
    refused("type equip to confirm", confirm: "")

    make_deploy(@project, 2, "queued")
    refused("deploy #2 is queued; wait for #2")
    @project.deploys.find_by!(number: 2).update!(status: "in_flight", kind: "restore")
    refused("restore #2 is in flight; wait for #2")
    @project.deploys.where(number: 2).delete_all

    run = @project.backup_runs.create!(location: storage_locations(:unas), kind: "auto", reason: "manual", status: "running", heartbeat_at: Time.current)
    refused("a backup of equip is running; wait for it")
    run.update!(status: "queued")
    refused("a backup of equip is queued; wait for it")
    run.update!(status: "go")
    assert_equal false, @project.deleting?

    ask
    refused("equip is already being deleted")
  end

  test "a deletion cancelled before removal, then asked again" do
    first = ask
    first.update!(status: "no_go", error: "cancelled: Docker didn't answer", finished_at: Time.current)
    assert_equal false, @project.reload.deleting?, "cancelled before removal: the project serves as before"

    second = ask
    assert_not_equal first, second
    assert_equal "queued", second.status
  end

  test "a deletion stopped during removal is resumed, not asked again" do
    first = ask
    first.update!(status: "no_go", removing_at: Time.current, step: "volumes", error: "stopped at volumes: busy", finished_at: Time.current)
    assert @project.reload.deleting?, "past the final snapshot, the project stays deleting"

    resumed = nil
    assert_enqueued_with(job: DeleteProjectJob) { resumed = ask(delete_backups: true) }
    assert_equal first, resumed
    assert_equal [ "queued", nil, nil ], [ resumed.status, resumed.error, resumed.finished_at ]
    assert_equal false, resumed.delete_backups, "a resumed deletion keeps what it was asked with"
  end

  # SQLite serializes the writes: whichever commits first wins, and the
  # other is refused inside its own transaction.
  test "a deploy and a delete at once" do
    Deploy.queue!(@project, sha: "b" * 40, ref: "refs/heads/main")
    refused("deploy #2 is queued")
    @project.deploys.where(number: 2).delete_all

    ask
    error = assert_raises(Project::Refused) { Deploy.queue!(@project, sha: "c" * 40, ref: "refs/heads/main") }
    assert_match "equip is being deleted", error.message
    assert_equal 0, @project.deploys.where(status: "queued").count
  end

  test "nothing new starts while a project is being deleted" do
    other = make_linked_project("equip-x")
    make_deploy(other, 1, "go")
    ask

    assert_match "equip is being deleted", assert_raises(Project::Refused) { Deploy.start!(@project, sha: "c" * 40, ref: "refs/heads/main") }.message
    assert_match "equip is being deleted", assert_raises(BackupRun::Refused) { BackupRun.request!(@project) }.message
    assert_match "equip is being deleted", assert_raises(Deploy::RestoreRefused) {
      Deploy.request_restore!(@project, snapshot: "33333333", location: storage_locations(:unas), confirm: "equip")
    }.message

    # A deploy queued some other way (never through request!'s checks) isn't claimed.
    @project.deploys.create!(number: 2, sha: "d" * 40, ref: "refs/heads/main", status: "queued", token_digest: "", heartbeat_at: Time.current)
    other_queued = Deploy.queue!(other, sha: "e" * 40, ref: "refs/heads/main")
    deploy, = Deploy.claim_next!(runner: "houston-runner-1")
    assert_equal other_queued, deploy
    assert_nil Deploy.claim_next!(runner: "houston-runner-2")

    moved = FakeGit.new { git_ok("#{"f" * 40}\trefs/heads/main\n") }
    use_fake_git(moved) do
      assert_equal [], ChangeCheck.new(@project).run
      assert_match "equip is being deleted", assert_raises(ChangeCheck::Failed) { ChangeCheck.new(@project).queue_head! }.message
    end
    assert_empty moved.calls, "a deleting project's repo isn't read"
  end

  test "the backup schedule skips a project being deleted" do
    @project.update!(volumes: [ { "name" => "storage", "path" => "/rails/storage" } ], backup_schedule: "daily 00:00")
    ask
    assert_nothing_raised { BackupScheduleJob.perform_now }
    assert_equal 0, @project.backup_runs.count
  end
end
