require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/fake_git"
require_relative "../support/backup_helpers"

class DeployRestoreTest < ActiveSupport::TestCase
  include ProjectHelpers
  include FakeDockerHelper
  include FakeGitHelper
  include BackupHelpers

  OLD = "d4e0b17" + "0" * 33

  setup do
    @project = make_backup_project
    @project.update!(repo_url: "git@forgejo:houston/equip.git", deploy_key_private: "-----BEGIN OPENSSH PRIVATE KEY-----\nk\n-----END OPENSSH PRIVATE KEY-----\n")
    @listing = FakeDocker.new { DockerCommand::Result.new(success: true, output: [ snapshot_json(id: "33333333", time: "2026-09-22T03:00:00Z", sha: OLD) ].to_json) }
  end

  def found_commit = FakeGit.new { |args| args.include?("rev-parse") ? git_ok("#{OLD}\n") : git_ok }

  def ask(confirm: "equip", snapshot: "33333333", location: storage_locations(:unas), git: found_commit)
    use_fake_git(git) { use_fake_docker(@listing) { Deploy.request_restore!(@project, snapshot:, location:, confirm:) } }
  end

  test "asking for a restore" do
    restore = ask
    assert_equal [ "restore", "queued", OLD, "restore:33333333", 2, "33333333".ljust(64, "0"), storage_locations(:unas) ],
                 [ restore.kind, restore.status, restore.sha, restore.ref, restore.generation, restore.source_snapshot_id, restore.source_location ]
    assert_equal 2, restore.number
  end

  test "a restore that can't be" do
    refused = ->(message, **options) do
      error = assert_raises(Deploy::RestoreRefused) { ask(**options) }
      assert_match message, error.message
      assert_equal 0, @project.deploys.where(kind: "restore").count, message
    end
    refused.("type equip to confirm", confirm: "Equip")
    refused.("isn't in unas-nfs", snapshot: "99999999")
    other = StorageLocation.create!(name: "b2-offsite", kind: "b2", settings: { "bucket" => "b" }, restic_password: "p", verified_at: Time.current, acknowledged_at: Time.current)
    refused.("never backed up to b2-offsite", location: other)
    refused.("commit d4e0b17 isn't in git@forgejo:houston/equip.git any more", git: FakeGit.new { |args| args.include?("fetch") ? git_failure("fatal: remote error: upload-pack: not our ref\n") : git_ok })

    make_deploy(@project, 2, "queued")
    refused.("wait for #2")
    @project.deploys.where(number: 2).delete_all
    @project.update!(repo_url: nil)
    refused.("link the repo first")
  end
end
