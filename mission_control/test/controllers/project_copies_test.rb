require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_git"
require_relative "../support/handover_helpers"

# Copying a project from Mission Control (docs/plans/copy-project.md,
# Batch 5): the confirm page, the copy's deploy page with Cancel, and what
# each project's page says afterwards, with Undo.
class ProjectCopiesTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeGitHelper
  include HandoverHelpers
  include ActiveJob::TestHelper

  HEAD = "c0bb1e5" + "0" * 33

  setup do
    @old = make_linked_project("equip-go", domains: %w[equip.svnmns.com])
    @old.update!(volumes: [ { "name" => "storage", "path" => "/rails/storage" } ], chosen_backup_location: storage_locations(:unas))
    @old.project_volumes.create!(name: "storage", location: storage_locations(:unas), placed_at: 1.day.ago)
    make_deploy(@old, 1, "go")
    make_deploy(@old, 2, "hold", sha: HEAD).update!(proposed_name: "equip")
    sign_in_as users(:one)
  end

  def repo
    sync = { name: "equip", app_service: "app", services: %w[app], domains: [], variables: [], health: "/up", port: 80,
             deploy_rule: { on: "commit", branch: "main" }, volumes: [ { name: "storage", path: "/rails/storage" } ] }.deep_stringify_keys
    FakeGit.new { |args| args.include?("rev-parse") ? git_ok("#{HEAD}\n") : (args.include?("inspect") ? git_ok({ "sync" => sync }.to_json) : nil) }
  end

  def copied
    use_fake_git(repo) { ProjectCopy.request!(@old, confirm: "equip-go", by: "one@example.com") }
  end

  test "the confirm page, and copying" do
    get new_project_copy_path("equip-go")
    assert_response :success
    assert_select ".restore__dialog[role=dialog] .restore__banner", /COPY · NEW NAME/
    assert_select "h1", "Copy equip-go to equip?"
    assert_select ".delete-list", /secrets.*storage \(unas-nfs\).*unas-nfs/m
    assert_select ".delete-list", /equip\.svnmns\.com/, "the hosts that move to equip"
    assert_select ".restore-note", /maintenance page is off.*stays in equip-go/m
    assert_select "label[for=confirm]", /Type equip-go to confirm/
    assert_select ".restore__actions", /houston copy --confirm equip-go/

    post project_copy_path("equip-go"), params: { confirm: "nope" }
    assert_response :unprocessable_entity
    assert_select ".field__error", /type equip-go to confirm/

    use_fake_git(repo) { post project_copy_path("equip-go"), params: { confirm: "equip-go" } }
    assert_redirected_to project_deploy_path("equip", 1)
    follow_redirect!
    assert_select "h1", /Copy #1/
    assert_select ".deploy-steps li", /Copy data/
    assert_select ".deploy-steps li", /Handover/
    assert_select "form[action=?] button", cancel_project_copy_path("equip"), text: "Cancel the copy"
  end

  test "cancelling from the copy's page" do
    copy = copied
    post cancel_project_copy_path("equip")
    assert_redirected_to project_deploy_path("equip", 1)
    assert_equal [ "no_go", "cancelled by one@example.com" ], [ copy.reload.status, copy.deploy.reload.error ]
    assert_enqueued_with(job: CopyCleanupJob)

    follow_redirect!
    assert_select "form[action=?]", cancel_project_copy_path("equip"), 0
  end

  test "after the copy: the old project says where it went; the new one can undo" do
    copy = copied
    copy.update!(status: "go", handed_over: %w[equip.svnmns.com], handed_over_at: Time.current)
    copy.deploy.update!(status: "go")

    get project_path("equip-go")
    assert_select ".notice--go", /equip-go was copied to equip.*delete equip-go when you're happy/mi
    assert_select ".notice--go a[href=?]", project_path("equip")

    get project_path("equip")
    assert_select ".notice--go", /copied from equip-go/
    assert_select "section#danger form[action=?]", undo_project_copy_path("equip")

    post undo_project_copy_path("equip"), params: { confirm: "nope" }
    assert_redirected_to project_path("equip")
    follow_redirect!
    assert_select ".notice--nogo", /type equip to confirm/

    deletion = nil
    with_handover(handover_doing) { post undo_project_copy_path("equip"), params: { confirm: "equip" } }
    deletion = copy.project.deletions.sole
    assert_redirected_to deletion_path(deletion)
    assert copy.reload.undone_at
  end

  test "signed out" do
    sign_out
    get new_project_copy_path("equip-go")
    assert_redirected_to sign_in_path
    post project_copy_path("equip-go"), params: { confirm: "equip-go" }
    assert_redirected_to sign_in_path
    assert_equal 0, ProjectCopy.count
  end

  test "a failed copy is said on the old project's page, with its log" do
    copy = copied
    copy.update!(status: "no_go", error: "kamal deploy failed (exit 1); the old version keeps serving",
                 log: "Build…\n ERROR web: attempt to write a readonly database (8)\n")
    get project_path("equip-go")
    assert_select ".notice--nogo", /The copy to equip failed: kamal deploy failed/
    assert_select ".notice--nogo details pre", /attempt to write a readonly database/
  end
end
