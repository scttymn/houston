require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/fake_git"
require_relative "../support/backup_helpers"

class ProjectRestoresTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeDockerHelper
  include FakeGitHelper
  include BackupHelpers

  OLD = "d4e0b17" + "0" * 33

  setup do
    @project = make_backup_project
    @project.update!(repo_url: "git@forgejo:houston/equip.git", deploy_key_private: "key\n")
    @listing = FakeDocker.new { DockerCommand::Result.new(success: true, output: [ snapshot_json(id: "33333333", time: "2026-09-22T03:00:00Z", sha: OLD, bytes: 404_000_000) ].to_json) }
    @git = FakeGit.new { |args| args.include?("rev-parse") ? git_ok("#{OLD}\n") : git_ok }
  end

  test "the confirm page and restoring" do
    sign_in_as users(:one)
    use_fake_docker(@listing) do
      get project_snapshots_path("equip")
      assert_select "a[href=?]", new_project_restore_path("equip", snapshot: "33333333", location: "unas-nfs"), text: "Restore"

      get new_project_restore_path("equip", snapshot: "33333333", location: "unas-nfs")
      assert_response :success
      assert_select "h1", /Roll equip back to 22 Sep 03:00\?/i
      assert_select ".restore-compare", /aaaaaaa.*d4e0b17/m
      assert_select ".restore-compare", /Live.*22 Sep 03:00 · auto · 385 MB/m
      assert_select ".restore-note", /keeps serving/
      assert_select ".restore-note", /maintenance page is off/i
      # The design's Restore: a dialog, with the comparison's NOW and AFTER RESTORE heads.
      assert_select ".restore__dialog[role=dialog] .restore__banner", /RESTORE · WHOLE PROJECT.*CODE \+ DATA/m
      assert_select ".restore-compare .restore-compare__head", %w[NOW AFTER\ RESTORE].size
      assert_select ".restore__phase", 2
      assert_select "input[name=confirm]"

      use_fake_git(@git) { post project_restores_path("equip"), params: { snapshot: "33333333", location: "unas-nfs", confirm: "Equip" } }
      assert_response :unprocessable_entity
      assert_select ".field__error", /type equip to confirm/
      assert_equal 0, @project.deploys.where(kind: "restore").count

      use_fake_git(@git) { post project_restores_path("equip"), params: { snapshot: "33333333", location: "unas-nfs", confirm: "equip" } }
    end
    restore = @project.deploys.find_by!(kind: "restore")
    assert_redirected_to project_deploy_path("equip", restore.number)
    follow_redirect!
    assert_select "h1", /Restore #2/i
  end

  test "restoring needs the admin" do
    post project_restores_path("equip"), params: { snapshot: "33333333", location: "unas-nfs", confirm: "equip" }
    assert_redirected_to new_session_path
    assert_equal 0, @project.deploys.where(kind: "restore").count
  end
end
