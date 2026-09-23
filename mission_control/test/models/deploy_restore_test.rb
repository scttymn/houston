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
    assert_equal [ "restore", "queued", OLD, "refs/restore/33333333", 2, "33333333".ljust(64, "0"), storage_locations(:unas) ],
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

  def claimed
    ask
    deploy, token, = Deploy.claim_next!(runner: "houston-runner-1")
    [ deploy, token ]
  end

  test "a claimed restore builds the next generation" do
    @project.update!(data_generation: 3)
    deploy, = claimed
    assert_equal [ "restore", 4, 3 ], [ deploy.kind, deploy.generation, deploy.previous_generation ]
    assert_nil Deploy.new(kind: "deploy", project: @project).previous_generation
  end

  # The flip is the step the runner reports once Kamal has switched traffic:
  # from then on the new generation is the one serving, and only then does
  # the runner remove the old one.
  test "the restore's generation becomes the project's once it's past the switch" do
    deploy, = claimed
    %w[Prepare Image Accessories Restore\ data Safety\ snapshot Switch].each do |step|
      deploy.report!(step:)
      assert_equal 1, @project.reload.data_generation, step
    end
    deploy.report!(step: "Clean up")
    # From the switch on, backups read generation 2, GO or not.
    assert_equal [ 2, 2 ], [ @project.reload.data_generation, @project.serving_generation.number ]
    deploy.report!(status: "go")
    assert_equal [ 2, 2 ], [ @project.reload.data_generation, @project.serving_generation.number ]
  end

  test "a restore GO flips it too (the Clean up report was lost)" do
    deploy, = claimed
    deploy.report!(status: "go")
    assert_equal 2, @project.reload.data_generation
  end

  test "a failed restore, or any ordinary deploy, leaves the generation" do
    deploy, = claimed
    deploy.report!(step: "Switch")
    deploy.report!(status: "no_go", error: "kamal deploy failed (exit 1); the old version keeps serving")
    assert_equal 1, @project.reload.data_generation

    ordinary, = Deploy.start!(@project, sha: "e" * 40, ref: "refs/heads/main")
    ordinary.update_column(:generation, 5) # whatever its row says
    ordinary.report!(step: "Clean up")
    ordinary.report!(status: "go")
    assert_equal 1, @project.reload.data_generation
  end

  test "the generation only ever goes forward" do
    deploy, = claimed
    @project.update!(data_generation: 3)
    deploy.report!(status: "go")
    assert_equal 3, @project.reload.data_generation
  end

  # Generation 2's containers (equip-db-g2, …) are claimed when the restore
  # is asked for, before any exists: a project by that name would share
  # Kamal's service label with them.
  test "a restore claims its generation's container names" do
    ask
    assert_equal %w[equip equip-cache equip-cache-g2 equip-db equip-db-g2], @project.hosts.pluck(:name).sort
  end

  test "a restore is refused when another project owns one of its names" do
    make_project("equip-db-g2")
    error = assert_raises(Deploy::RestoreRefused) { ask }
    assert_equal "the container name equip-db-g2 belongs to project equip-db-g2; rename a service or that project first", error.message
    assert_equal 0, @project.deploys.where(kind: "restore").count
  end

  # The restore's compose.yml, kept with it by its check sync, becomes the
  # project's in the same transaction as the flip: from the switch on,
  # Mission Control describes what serves.
  test "the flip applies the restore's compose.yml" do
    deploy, = claimed
    payload = { name: "equip", app_service: "app", services: %w[app db], domains: [], variables: [], health: "/up", port: 80,
                deploy_rule: { on: "commit", branch: "main", tags: "v*" }, volumes: [ { name: "media", path: "/media" } ] }.as_json
    deploy.update!(sync_payload: payload)
    deploy.report!(step: "Switch")
    assert_equal [ { "name" => "storage", "path" => "/rails/storage" } ], @project.reload.volumes
    deploy.report!(step: "Clean up")
    assert_equal [ { "name" => "media", "path" => "/media" } ], @project.reload.volumes
    assert_equal %w[app db], @project.services
    assert_equal %w[equip equip-db-g2], @project.hosts.pluck(:name).sort
    assert_kind_of ProjectSync, deploy.adopted

    # GO after it doesn't apply it again.
    @project.update!(volumes: [])
    deploy.report!(status: "go")
    assert_equal [], @project.reload.volumes
  end

  # The flip applying a compose.yml that can't be applied (another project
  # took one of its names) is logged, never a rollback of the flip: traffic
  # has moved, and a refused Clean up would leave the restore to go stale.
  test "a compose.yml that can't be applied doesn't undo the flip" do
    deploy, = claimed
    make_project("equip-search-g2")
    payload = { name: "equip", app_service: "app", services: %w[app search], domains: [], variables: [], health: "/up", port: 80,
                deploy_rule: { on: "commit", branch: "main", tags: "v*" } }.as_json
    deploy.update!(sync_payload: payload)
    deploy.report!(step: "Clean up")
    assert_equal 2, @project.reload.data_generation
    assert deploy.reload.switched_at
    assert_nil deploy.adopted
    assert_equal %w[app db cache], @project.services
  end

  # A restore that switched but never got GO still labels backups with its
  # commit: it's the running deploy.
  test "a switched restore is the running deploy" do
    deploy, = claimed
    assert_equal 1, @project.running_deploy.number
    deploy.report!(step: "Clean up")
    assert_equal deploy.number, @project.reload.running_deploy.number
    deploy.report!(status: "no_go", error: "abandoned")
    assert_equal deploy.number, @project.reload.running_deploy.number

    # A newer restore of the same generation that never switched isn't it.
    @project.deploys.create!(number: 9, sha: "f" * 40, ref: "refs/restore/99999999", status: "no_go", kind: "restore", token_digest: "",
                             heartbeat_at: Time.current, generation: 2)
    assert_equal deploy.number, @project.reload.running_deploy.number

    later, = Deploy.start!(@project, sha: "e" * 40, ref: "refs/heads/main")
    later.report!(status: "go")
    assert_equal later.number, @project.reload.running_deploy.number
  end
end
