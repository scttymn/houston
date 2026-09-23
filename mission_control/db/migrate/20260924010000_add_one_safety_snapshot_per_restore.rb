# Build step 6, Batch 6: a restore's safety snapshot (reason restore), one per
# restore deploy, as the pre-deploy snapshot is one per deploy.
class AddOneSafetySnapshotPerRestore < ActiveRecord::Migration[8.1]
  def change
    add_index :backup_runs, [ :project_id, :deploy_number ], unique: true, where: "operation = 'backup' AND reason = 'restore'",
                                                             name: "index_backup_runs_one_safety_snapshot_per_restore"
  end
end
