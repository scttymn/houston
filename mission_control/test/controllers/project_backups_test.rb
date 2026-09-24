require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/backup_helpers"

class ProjectBackupsTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers
  include ActiveJob::TestHelper

  setup do
    @project = make_project("equip")
    @project.update!(volumes: [ { "name" => "storage", "path" => "/rails/storage" } ])
    make_deploy(@project, 1, "go")
  end

  test "create snapshot" do
    sign_in_as users(:one)
    get project_path("equip")
    assert_select ".snapshots .panel__head form[action='/projects/equip/backups'] button", "Create"

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

  test "create snapshot needs something to back up and somewhere to put it" do
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

  test "create snapshot needs the admin" do
    post project_backups_path("equip")
    assert_redirected_to sign_in_path
    assert_equal 0, BackupRun.count
  end

  test "the backup plan" do
    sign_in_as users(:one)
    @project.update!(services: %w[app db cache], databases: [ { "service" => "db", "image" => "postgres:17" } ], keep_auto: 7, keep_deploy: 3)
    @project.backup_runs.create!(location: storage_locations(:unas), kind: "auto", reason: "manual", status: "go", heartbeat_at: Time.current,
                                 found: { "databases" => [], "sqlite" => [ { "volume" => "storage", "path" => "production.sqlite3" } ] })
    @project.backup_runs.create!(location: storage_locations(:unas), kind: "auto", reason: "manual", status: "no_go", heartbeat_at: Time.current, found: {})

    # The plan is Snapshots' Settings tab, not a section of its own.
    get project_path("equip")
    assert_select "turbo-frame#snapshots-list[src='/projects/equip/snapshots'][loading=lazy]"
    assert_select ".backup-plan", 0
    assert_select ".section-nav a[href='#backup-plan']", 0

    Installation.current.update!(time_zone: "Europe/Berlin")
    @project.update!(backup_schedule: "daily 22:15")
    # The tabs are the same on every tab, counts included, so they don't jump.
    listing = FakeDocker.new { DockerCommand::Result.new(success: true, output: [ snapshot_json(id: "11111111", time: "2026-09-21T03:00:00Z") ].to_json) }
    use_fake_docker(listing) { get project_snapshots_path("equip", kind: "settings") }
    assert_response :success
    assert_select "#snapshots-list [role=tablist] a[aria-selected=true]", /Settings/
    assert_select "#snapshots-list [role=tablist] a", /Scheduled\s*1 \/ 7/
    assert_select "#snapshots-list [role=tablist] a", /Pre-deploy\s*0 \/ 3/
    # Each tab keeps room for its bold label, so selecting one doesn't shift the rest.
    assert_select "#snapshots-list [role=tablist] a .tabs__label[data-label]", 3

    # Settings still shows when the repository can't be read; only the counts go.
    Snapshots.forget_cache(@project, @project.backup_location) # the listing above is cached
    failing = FakeDocker.new { failure("Fatal: unable to open repository at /repo: permission denied\n") }
    use_fake_docker(failing) { get project_snapshots_path("equip", kind: "settings") }
    assert_response :success
    assert_select "#snapshots-list .snapshot-settings"
    assert_select "#snapshots-list [role=tablist] a .mono", 0
    plan = "#snapshots-list .snapshot-settings"
    assert_select plan, /Edit in compose\.yml/
    assert_select plan, /7 scheduled · 3 pre-deploy/
    assert_select plan, %r{Daily at 22:15 \(Europe/Berlin\)}
    assert_select plan, /unas-nfs/
    assert_select plan, %r{storage.*/rails/storage}m
    assert_select plan, /db.*pg_dump/m
    assert_select plan, /production\.sqlite3.*\.backup/m
    assert_select plan, /NOT BACKED UP.*cache/m
  end

  test "choosing a project's backup target" do
    sign_in_as users(:one)
    offsite = StorageLocation.create!(name: "b2-offsite", kind: "b2", settings: { "bucket" => "sm" }, restic_password: "pw", verified_at: Time.current, acknowledged_at: Time.current)
    StorageLocation.create!(name: "later", kind: "b2", settings: { "bucket" => "sm2" }, restic_password: "pw", verified_at: Time.current)

    use_fake_docker(FakeDocker.new { DockerCommand::Result.new(success: true, output: "[]") }) { get project_snapshots_path("equip", kind: "settings") }
    assert_select ".snapshot-settings select[name=location] option", 2 # the default, b2-offsite
    assert_select ".snapshot-settings form[data-turbo-frame=_top]"
    patch project_backup_target_path("equip"), params: { location: "b2-offsite" }
    # Back on the Settings tab, where the choice was made.
    assert_redirected_to project_path("equip", snapshots: "settings", anchor: "snapshots")
    assert_equal offsite, @project.reload.backup_location
    follow_redirect!
    assert_select "turbo-frame#snapshots-list[src='/projects/equip/snapshots?kind=settings']"
    use_fake_docker(FakeDocker.new { DockerCommand::Result.new(success: true, output: "[]") }) { get project_snapshots_path("equip", kind: "settings") }
    assert_select ".snapshot-settings", /Backing up to b2-offsite\. Earlier snapshots stay where they were written/
    assert_equal offsite, BackupRun.request!(@project).location

    patch project_backup_target_path("equip"), params: { location: "later" }
    follow_redirect!
    assert_select ".notice--nogo", /later isn't set up yet/
    assert_equal offsite, @project.reload.backup_location

    patch project_backup_target_path("equip"), params: { location: "" }
    assert_equal storage_locations(:unas), @project.reload.backup_location
  end
end
