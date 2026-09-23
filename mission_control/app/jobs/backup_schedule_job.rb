# Every minute: queue each project's scheduled backup once it's due, once
# per local day. A tick after downtime catches up the same day; a whole day
# missed is skipped. Only queues, so it stays on the default queue.
class BackupScheduleJob < ApplicationJob
  queue_as :default

  def perform
    zone = Installation.current.zone
    now = Time.current
    Project.find_each do |project|
      next if project.volumes.empty? && project.databases.empty?
      next unless project.running_deploy && project.backup_location

      schedule = BackupSchedule.new(project, zone)
      BackupRun.request!(project, reason: "schedule", scheduled_for: schedule.today(now)) if schedule.due?(now)
    end
  end
end
