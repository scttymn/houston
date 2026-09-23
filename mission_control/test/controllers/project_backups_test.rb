require "test_helper"
require_relative "../support/project_helpers"

class ProjectBackupsTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include ActiveJob::TestHelper

  setup do
    @project = make_project("equip")
    @project.update!(volumes: [ { "name" => "storage", "path" => "/rails/storage" } ])
    make_deploy(@project, 1, "go")
  end

  test "back up now" do
    sign_in_as users(:one)
    get project_path("equip")
    assert_select "form[action='/projects/equip/backups'] button", "Back up now"

    assert_enqueued_jobs(1, only: BackupJob) do
      post project_backups_path("equip")
      post project_backups_path("equip")
    end
    assert_redirected_to project_path("equip")
    assert_equal 1, BackupRun.count

    follow_redirect!
    assert_select "[data-backup]", /Backing up/

    # Mission Control stopped mid-backup: after 2 minutes of silence the page
    # says so, without waiting for the next backup to notice.
    BackupRun.last.update!(status: "running", heartbeat_at: 3.minutes.ago)
    get project_path("equip")
    assert_select "[data-backup]", /NO-GO.*Mission Control stopped during the backup/m

    BackupRun.last.update!(status: "go", snapshot_id: "5c5edd4c" + "0" * 56, bytes: 410_000_000, finished_at: Time.current)
    get project_path("equip")
    assert_select "[data-backup]", /GO.*5c5edd4c.*391 MB/m

    BackupRun.last.update!(status: "no_go", error: "restic backup failed: unable to open repository")
    get project_path("equip")
    assert_select "[data-backup]", /NO-GO.*unable to open repository/m

    post project_backups_path("nope")
    assert_response :not_found
  end

  test "back up now needs something to back up and somewhere to put it" do
    sign_in_as users(:one)
    fresh = make_project("fresh")
    post project_backups_path("fresh")
    assert_redirected_to project_path("fresh")
    follow_redirect!
    assert_select ".notice", /nothing deployed yet/
    assert_equal 0, BackupRun.count

    StorageLocation.update_all(acknowledged_at: nil)
    get project_path("equip")
    assert_select "form[action='/projects/equip/backups']", 0
    post project_backups_path("equip")
    assert_equal 0, BackupRun.count
    assert fresh
  end

  test "back up now needs the admin" do
    post project_backups_path("equip")
    assert_redirected_to new_session_path
    assert_equal 0, BackupRun.count
  end
end
