# Build step 5, Batch 6: where each of a project's named volumes lives
# (local disk, or a location that holds live volumes), and when Houston made it.
class CreateProjectVolumes < ActiveRecord::Migration[8.1]
  def change
    create_table :project_volumes do |t|
      t.references :project, null: false, foreign_key: true
      t.references :location, foreign_key: { to_table: :storage_locations }
      t.string :name, null: false
      t.datetime :placed_at
      t.timestamps
    end
    add_index :project_volumes, [ :project_id, :name ], unique: true
  end
end
