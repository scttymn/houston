require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/backup_helpers"

class BackupScheduleJobTest < ActiveJob::TestCase
  include ProjectHelpers
  include BackupHelpers

  setup do
    @project = make_backup_project
    Installation.current.update!(time_zone: "Europe/Berlin")
  end

  def tick(at) = travel_to(at) { BackupScheduleJob.perform_now }

  def scheduled = BackupRun.where(reason: "schedule")

  test "the schedule tick" do
    tick(Time.utc(2026, 9, 23, 0, 59)) # 02:59 in Berlin
    assert_equal 0, scheduled.count, "not yet due"

    assert_enqueued_jobs(1, only: BackupJob) do
      tick(Time.utc(2026, 9, 23, 1, 0))
      tick(Time.utc(2026, 9, 23, 1, 1)) # ticked again: still one
    end
    assert_equal [ [ Date.new(2026, 9, 23), "queued" ] ], scheduled.pluck(:scheduled_for, :status)

    # Mission Control was down at 03:00 on the 24th: the first tick after catches up.
    scheduled.update_all(status: "go")
    tick(Time.utc(2026, 9, 24, 14, 0))
    assert_equal [ Date.new(2026, 9, 23), Date.new(2026, 9, 24) ], scheduled.order(:scheduled_for).pluck(:scheduled_for)

    # A whole day missed (the 25th) isn't made up on the 26th: one run for the 26th.
    scheduled.update_all(status: "go")
    tick(Time.utc(2026, 9, 26, 2, 0))
    assert_equal [ Date.new(2026, 9, 23), Date.new(2026, 9, 24), Date.new(2026, 9, 26) ], scheduled.order(:scheduled_for).pluck(:scheduled_for)
  end

  test "the tick skips projects it can't back up" do
    never = make_project("never")
    never.update!(volumes: [ { "name" => "data", "path" => "/data" } ])
    empty = make_project("empty")
    make_deploy(empty, 1, "go")
    tick(Time.utc(2026, 9, 23, 2, 0))
    assert_equal [ "equip" ], scheduled.map { |r| r.project.name }

    StorageLocation.update_all(acknowledged_at: nil)
    scheduled.delete_all
    tick(Time.utc(2026, 9, 23, 2, 0))
    assert_equal 0, scheduled.count
  end

  test "production runs the tick every minute" do
    recurring = YAML.load(ERB.new(Rails.root.join("config/recurring.yml").read).result, aliases: true)
    assert_equal({ "class" => "BackupScheduleJob", "schedule" => "every minute" }, recurring.dig("production", "schedule_backups"))
  end
end
