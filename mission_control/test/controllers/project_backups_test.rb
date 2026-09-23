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

    # No acknowledged storage: setup isn't finished, so its gate answers
    # first (BackupRunTest covers the model's own refusal).
    StorageLocation.update_all(acknowledged_at: nil)
    post project_backups_path("equip")
    assert_redirected_to setup_storage_path
    assert_equal 0, BackupRun.count
    assert fresh
  end

  test "back up now needs the admin" do
    post project_backups_path("equip")
    assert_redirected_to new_session_path
    assert_equal 0, BackupRun.count
  end

  test "the backup plan" do
    sign_in_as users(:one)
    @project.update!(services: %w[app db cache], databases: [ { "service" => "db", "image" => "postgres:17" } ], keep_auto: 7, keep_deploy: 3)
    @project.backup_runs.create!(location: storage_locations(:unas), kind: "auto", reason: "manual", status: "go", heartbeat_at: Time.current,
                                 found: { "databases" => [], "sqlite" => [ { "volume" => "storage", "path" => "production.sqlite3" } ] })
    @project.backup_runs.create!(location: storage_locations(:unas), kind: "auto", reason: "manual", status: "no_go", heartbeat_at: Time.current, found: {})

    get project_path("equip")
    assert_select "turbo-frame#snapshots[src='/projects/equip/snapshots'][loading=lazy]"
    assert_select ".backup-plan", /7 scheduled · 3 pre-deploy/
    Installation.current.update!(time_zone: "Europe/Berlin")
    @project.update!(backup_schedule: "daily 22:15")
    get project_path("equip")
    assert_select ".backup-plan", %r{Daily at 22:15 \(Europe/Berlin\)}
    assert_select ".backup-plan", /unas-nfs/
    assert_select ".backup-plan", %r{storage.*/rails/storage}m
    assert_select ".backup-plan", /db.*pg_dump/m
    assert_select ".backup-plan", /production\.sqlite3.*\.backup/m
    assert_select ".backup-plan", /NOT BACKED UP.*cache/m
  end
end
