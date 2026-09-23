# Build step 5, Batch 4: each project's schedule (from sync), Houston's time
# zone, and the local date a scheduled backup was for (one per day).
class AddBackupSchedule < ActiveRecord::Migration[8.1]
  def change
    add_column :projects, :backup_schedule, :string, default: "daily 03:00", null: false
    add_column :installations, :time_zone, :string, default: "UTC", null: false
    add_column :backup_runs, :scheduled_for, :date
    add_index :backup_runs, [ :project_id, :scheduled_for ], unique: true, where: "scheduled_for IS NOT NULL", name: "index_backup_runs_one_scheduled_per_day"
  end
end
