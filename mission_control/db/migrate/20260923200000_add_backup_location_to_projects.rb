# Build step 5, Batch 7: a project's own backup target (null: the default).
class AddBackupLocationToProjects < ActiveRecord::Migration[8.1]
  def change
    add_reference :projects, :backup_location, foreign_key: { to_table: :storage_locations }
  end
end
