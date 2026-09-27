# Deleting a project (docs/plans/delete-project.md): the request and, once
# the project's row is gone, the record of it (its name, repo and final
# snapshot). One queued or running per project: the partial unique index.
class CreateProjectDeletions < ActiveRecord::Migration[8.1]
  def change
    create_table :project_deletions do |t|
      t.references :project, foreign_key: { on_delete: :nullify }
      t.string :name, null: false
      t.string :repo_url
      t.string :by, null: false
      t.boolean :delete_backups, null: false, default: false
      t.string :status, null: false, default: "queued"
      t.string :step
      t.text :log
      t.string :error, limit: 4000
      t.string :snapshot_id
      t.references :snapshot_location, foreign_key: { to_table: :storage_locations }
      # Past the final snapshot: removal has begun, and the project serves nothing new.
      t.datetime :removing_at
      t.datetime :started_at
      t.datetime :finished_at
      t.datetime :heartbeat_at, null: false
      t.timestamps
    end
    add_index :project_deletions, :name
    add_index :project_deletions, :project_id, unique: true, where: "status IN ('queued', 'running')", name: "index_project_deletions_one_active"
  end
end
