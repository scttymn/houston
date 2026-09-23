class CreateStorageLocations < ActiveRecord::Migration[8.1]
  def change
    create_table :storage_locations do |t|
      t.string :name, null: false, index: { unique: true }
      t.string :kind, null: false
      t.text :settings
      t.text :credentials
      t.text :restic_password
      t.datetime :verified_at
      t.datetime :acknowledged_at
      t.boolean :default, null: false, default: false
      t.timestamps
    end
  end
end
