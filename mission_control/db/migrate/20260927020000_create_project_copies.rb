# Copying a project to a new name (docs/plans/copy-project.md): the request,
# then the record of it. One queued or running per old project.
class CreateProjectCopies < ActiveRecord::Migration[8.1]
  def change
    create_table :project_copies do |t|
      t.references :from_project, foreign_key: { to_table: :projects, on_delete: :nullify }
      t.references :project, foreign_key: { on_delete: :nullify }
      t.references :deploy, foreign_key: { on_delete: :nullify }
      t.references :snapshot_run, foreign_key: { to_table: :backup_runs, on_delete: :nullify }
      t.string :from, null: false
      t.string :to, null: false
      t.string :sha, null: false
      t.string :by, null: false
      t.string :status, null: false, default: "queued"
      t.string :error, limit: 4000
      # The shared hosts moved to the new project: the way back (Undo copy).
      t.json :handed_over, null: false, default: []
      t.datetime :handed_over_at
      t.datetime :undone_at
      t.timestamps
    end
    add_index :project_copies, :from_project_id, unique: true, where: "status IN ('queued', 'running')", name: "index_project_copies_one_active"
    add_index :project_copies, :from
  end
end
