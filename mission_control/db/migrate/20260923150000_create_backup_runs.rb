# Build step 5: what a project's backups hold (from sync), and each backup
# run. Snapshots themselves are read from restic; this records the runs.
class CreateBackupRuns < ActiveRecord::Migration[8.1]
  def change
    add_column :projects, :volumes, :json, default: [], null: false
    add_column :projects, :databases, :json, default: [], null: false

    create_table :backup_runs do |t|
      t.references :project, null: false, foreign_key: true
      t.references :location, null: false, foreign_key: { to_table: :storage_locations }
      t.string :kind, null: false
      t.string :reason, null: false
      t.integer :deploy_number
      t.string :status, null: false, default: "queued"
      t.string :sha
      t.string :token_digest, null: false, default: ""
      t.datetime :heartbeat_at, null: false
      t.datetime :started_at
      t.datetime :finished_at
      t.string :snapshot_id
      t.bigint :bytes
      t.json :found, default: {}, null: false
      t.string :error, limit: 4000
      t.text :log
      t.timestamps
    end
    add_index :backup_runs, :project_id, unique: true, where: "status = 'running'", name: "index_backup_runs_one_running"
    add_index :backup_runs, :project_id, unique: true, where: "status = 'queued' AND reason = 'manual'", name: "index_backup_runs_one_queued_manual"
    add_index :backup_runs, [ :project_id, :created_at ]
  end
end
