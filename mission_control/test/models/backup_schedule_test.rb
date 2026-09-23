require "test_helper"
require_relative "../support/project_helpers"

class BackupScheduleTest < ActiveSupport::TestCase
  include ProjectHelpers

  setup do
    @project = make_project("equip")
    @project.update!(backup_schedule: "daily 03:00")
  end

  def schedule(zone) = BackupSchedule.new(@project, ActiveSupport::TimeZone[zone])

  test "when a project is due" do
    utc = schedule("UTC")
    assert_equal Time.utc(2026, 9, 23, 3, 0), utc.due_at(Date.new(2026, 9, 23))
    berlin = schedule("Europe/Berlin")
    assert_equal Time.utc(2026, 9, 23, 1, 0), berlin.due_at(Date.new(2026, 9, 23)), "03:00 CEST"

    # New York's spring-forward gap: 02:30 doesn't exist, so 03:30 EDT.
    @project.update!(backup_schedule: "daily 02:30")
    ny = schedule("America/New_York")
    assert_equal Time.utc(2026, 3, 8, 7, 30), ny.due_at(Date.new(2026, 3, 8))
    # Fall-back: 01:30 happens twice; the first one.
    @project.update!(backup_schedule: "daily 01:30")
    assert_equal Time.utc(2026, 11, 1, 5, 30), schedule("America/New_York").due_at(Date.new(2026, 11, 1))

    @project.update!(backup_schedule: "daily 03:00")
    assert_not berlin.due?(Time.utc(2026, 9, 23, 0, 59)), "02:59 in Berlin"
    assert berlin.due?(Time.utc(2026, 9, 23, 1, 0))
    assert_equal Date.new(2026, 9, 23), berlin.today(Time.utc(2026, 9, 22, 23, 30)), "01:30 on the 23rd in Berlin"
    assert_equal Time.utc(2026, 9, 23, 1, 0), berlin.next_at(Time.utc(2026, 9, 22, 23, 30))

    @project.backup_runs.create!(location: storage_locations(:unas), kind: "auto", reason: "schedule", status: "go", scheduled_for: Date.new(2026, 9, 23), heartbeat_at: Time.current)
    assert_not berlin.due?(Time.utc(2026, 9, 23, 12, 0)), "today's has run"
    assert_equal Time.utc(2026, 9, 24, 1, 0), berlin.next_at(Time.utc(2026, 9, 23, 12, 0)), "so tomorrow's is next"
  end
end
