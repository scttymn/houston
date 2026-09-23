class CreateProjects < ActiveRecord::Migration[8.1]
  def change
    create_table :projects do |t|
      t.string :name, null: false
      t.string :app_service, null: false
      t.json :services, null: false, default: []
      t.json :domains, null: false, default: []
      t.json :variables, null: false, default: []
      t.string :health, null: false
      t.integer :port, null: false
      t.json :deploy_rule, null: false, default: {}
      t.datetime :synced_at
      t.timestamps
    end
    add_index :projects, :name, unique: true

    # Container-name prefixes a project owns on the server: its name and
    # <name>-<service> for each accessory. The unique index keeps two
    # projects from ever sharing one, even when they sync at the same time.
    create_table :project_hosts do |t|
      t.references :project, null: false, foreign_key: { on_delete: :cascade }
      t.string :name, null: false
      t.timestamps
    end
    add_index :project_hosts, :name, unique: true

    create_table :secrets do |t|
      t.references :project, null: false, foreign_key: { on_delete: :cascade }
      t.string :key, null: false
      t.text :value
      t.timestamps
    end
    add_index :secrets, [ :project_id, :key ], unique: true
  end
end
