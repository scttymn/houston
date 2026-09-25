# Updates of this server started from Mission Control
# (docs/plans/update-from-mission-control.md). At most one runs at a time:
# the partial unique index is the lock.
class CreateServerUpdates < ActiveRecord::Migration[8.1]
  def change
    create_table :server_updates do |t|
      t.string :to_version, null: false
      t.string :from_version, null: false
      t.string :status, null: false, default: "running"
      t.datetime :started_at, null: false
      t.datetime :finished_at
      t.text :log
      t.timestamps
    end
    add_index :server_updates, :status, unique: true, where: "status = 'running'", name: "index_server_updates_one_running"
  end
end
