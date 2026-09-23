# Build step 5, Batch 5: one pre-deploy snapshot per deploy, however often
# the runner asks.
class AddOneSnapshotPerDeploy < ActiveRecord::Migration[8.1]
  def change
    add_index :backup_runs, [ :project_id, :deploy_number ], unique: true, where: "reason = 'deploy'", name: "index_backup_runs_one_per_deploy"
  end
end
