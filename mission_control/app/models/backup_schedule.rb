# When a project's scheduled backup is due: "daily HH:MM" in Houston's time
# zone. A time in a spring-forward gap runs at the first valid time after
# it; a repeated hour runs once (the first). One scheduled run per local day.
class BackupSchedule
  FORMAT = /\Adaily ([01][0-9]|2[0-3]):([0-5][0-9])\z/

  def initialize(project, zone)
    @project = project
    @zone = zone
    @hour, @minute = project.backup_schedule.match(FORMAT).captures.map(&:to_i)
  end

  def today(now) = now.in_time_zone(@zone).to_date

  def due_at(date) = @zone.local(date.year, date.month, date.day, @hour, @minute)

  def due?(now)
    date = today(now)
    now >= due_at(date) && !ran_on?(date)
  end

  def next_at(now)
    date = today(now)
    ran_on?(date) ? due_at(date + 1) : due_at(date)
  end

  # "Daily at 03:00 (Europe/Berlin)"
  def words = format("Daily at %02d:%02d (%s)", @hour, @minute, @zone.name)

  private
    def ran_on?(date) = @project.backup_runs.exists?(scheduled_for: date)
end
