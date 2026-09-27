require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/backup_helpers"

# Deleting a project from Mission Control (docs/plans/delete-project.md,
# Batch 4): the Danger zone, the confirm page, the deletion's page.
class ProjectDeletionsTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include BackupHelpers
  include ActiveJob::TestHelper

  setup do
    @project = make_backup_project
    @project.update!(repo_url: "git@forgejo:houston/equip.git", domains: %w[equipping.com])
    @project.project_volumes.create!(name: "storage", location: storage_locations(:unas))
  end

  test "the Danger zone and the confirm page" do
    sign_in_as users(:one)
    get project_path("equip")
    assert_select ".section-nav a:last-child", "Danger zone"
    assert_select "section#danger .panel__title", "Danger zone"
    assert_select "section#danger a[href=?]", new_project_deletion_path("equip"), text: "Delete this project"

    get new_project_deletion_path("equip")
    assert_response :success
    assert_select ".restore__dialog[role=dialog] .restore__banner", /DELETE · WHOLE PROJECT/
    assert_select "h1", "Delete equip?"
    assert_select ".delete-list", /equip-web.*equip-db.*equip-cache/m
    assert_select ".delete-list", /storage.*unas-nfs/m
    assert_select ".delete-list", /equip\.svnmns\.com.*equipping\.com/m
    assert_select ".restore-note", /final snapshot.*unas-nfs/m
    assert_select "input[type=checkbox][name=delete_backups]"
    assert_select "label[for=confirm]", /Type equip to confirm/
    assert_select ".restore__actions", /houston delete --confirm equip/
    assert_select "input[type=submit][value=?]", "Delete equip"
  end

  test "deleting, the wrong name and then the right one" do
    sign_in_as users(:one)
    assert_no_enqueued_jobs(only: DeleteProjectJob) { post project_deletion_path("equip"), params: { confirm: "Equip" } }
    assert_response :unprocessable_entity
    assert_select ".field__error", /type equip to confirm/
    assert_equal 0, ProjectDeletion.count

    make_deploy(@project, 2, "in_flight")
    post project_deletion_path("equip"), params: { confirm: "equip" }
    assert_response :unprocessable_entity
    assert_select ".field__error", /deploy #2 is in flight; wait for #2/
    @project.deploys.where(number: 2).delete_all

    assert_enqueued_with(job: DeleteProjectJob) { post project_deletion_path("equip"), params: { confirm: "equip", delete_backups: "1" } }
    deletion = ProjectDeletion.sole
    assert_redirected_to deletion_path(deletion)
    assert_equal [ "one@example.com", true ], [ deletion.by, deletion.delete_backups ]

    follow_redirect!
    assert_select "h1", /Deleting equip/
    assert_select ".deploy-steps li", ProjectRemoval::REMOVAL_STEPS.size + 2
    assert_select "[data-controller=refresh]"
  end

  test "the deletion's page once it's done" do
    sign_in_as users(:one)
    deletion = ProjectDeletion.create!(project: @project, name: "equip", repo_url: "git@forgejo:houston/equip.git", by: "one@example.com",
                                       status: "running", heartbeat_at: Time.current, started_at: 1.minute.ago, removing_at: Time.current,
                                       snapshot_id: "5c5edd4c" + "0" * 56, snapshot_location: storage_locations(:unas), step: "rows")
    @project.destroy!
    deletion.update!(status: "go", finished_at: Time.current, log: "== rows\nGO: equip is deleted\n")

    get deletion_path(deletion)
    assert_response :success
    assert_select ".notice--go", /equip was deleted/
    assert_select ".notice--go", /final snapshot 5c5edd4c in unas-nfs/
    assert_select ".notice--go", /houston restore 5c5edd4c/
    assert_select ".notice--hold", /deploy key and the webhook.*git@forgejo:houston\/equip\.git/m
    assert_select "[data-controller=refresh]", 0
    assert_select ".log", /GO: equip is deleted/
  end

  test "the project page while it's being deleted" do
    sign_in_as users(:one)
    deletion = ProjectDeletion.create!(project: @project, name: "equip", by: "one@example.com", status: "running", step: "volumes", heartbeat_at: Time.current)
    get project_path("equip")
    assert_select ".notice--hold", /equip is being deleted/
    assert_select ".notice--hold a[href=?]", deletion_path(deletion)
    assert_select "section#danger a", 0

    deletion.update!(status: "no_go", removing_at: Time.current, error: "stopped at volumes: volume is in use", finished_at: Time.current)
    get project_path("equip")
    assert_select ".notice--nogo", /Deleting equip stopped at volumes: volume is in use/
    assert_select ".notice--nogo form[action=?] button[type=submit]", project_deletion_path("equip"), text: "Finish deleting"

    assert_enqueued_with(job: DeleteProjectJob) { post project_deletion_path("equip"), params: { confirm: "equip" } }
    assert_equal [ "queued", deletion.id ], [ deletion.reload.status, ProjectDeletion.sole.id ]
  end

  test "a cancelled deletion is said on the project page" do
    sign_in_as users(:one)
    ProjectDeletion.create!(project: @project, name: "equip", by: "one@example.com", status: "no_go", error: "cancelled: Docker didn't answer", heartbeat_at: Time.current, finished_at: Time.current)
    get project_path("equip")
    assert_select ".notice--nogo", /Deleting equip was cancelled: Docker didn't answer\. Nothing was removed/
    assert_select "section#danger a", "Delete this project"
  end

  test "signed out" do
    get new_project_deletion_path("equip")
    assert_redirected_to sign_in_path
    post project_deletion_path("equip"), params: { confirm: "equip" }
    assert_redirected_to sign_in_path
    assert_equal 0, ProjectDeletion.count

    deletion = ProjectDeletion.create!(project: @project, name: "equip", by: "one@example.com", heartbeat_at: Time.current)
    get deletion_path(deletion)
    assert_redirected_to sign_in_path
  end
end
