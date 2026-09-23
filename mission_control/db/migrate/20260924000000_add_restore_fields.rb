# Build step 6, Batch 4: a restore is a deploy of kind restore, reading a
# snapshot from a location; its data is restored by a run of operation
# restore, one per restore deploy.
class AddRestoreFields < ActiveRecord::Migration[8.1]
  def change
    add_column :deploys, :kind, :string, default: "deploy", null: false
    add_column :deploys, :source_snapshot_id, :string
    add_reference :deploys, :source_location, foreign_key: { to_table: :storage_locations }
    add_column :backup_runs, :operation, :string, default: "backup", null: false
    add_column :backup_runs, :source_snapshot_id, :string
    add_index :backup_runs, [ :project_id, :deploy_number ], unique: true, where: "operation = 'restore'", name: "index_backup_runs_one_restore_per_deploy"
  end
end
