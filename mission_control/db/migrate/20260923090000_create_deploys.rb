class CreateDeploys < ActiveRecord::Migration[8.1]
  def change
    create_table :deploys do |t|
      t.references :project, null: false, foreign_key: { on_delete: :cascade }
      t.integer :number, null: false
      t.string :sha, null: false
      t.string :ref, null: false
      t.string :status, null: false, default: "in_flight"
      t.string :step
      t.text :log, null: false, default: ""
      t.string :error
      t.string :token_digest, null: false
      t.datetime :heartbeat_at, null: false
      t.datetime :finished_at
      t.timestamps
    end
    add_index :deploys, [ :project_id, :number ], unique: true
    # One deploy in flight per project, even if two requests race.
    add_index :deploys, :project_id, unique: true, where: "status = 'in_flight'", name: "index_deploys_one_in_flight"
  end
end
