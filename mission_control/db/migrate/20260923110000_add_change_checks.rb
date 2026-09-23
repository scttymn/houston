class AddChangeChecks < ActiveRecord::Migration[8.1]
  def change
    change_table :projects do |t|
      t.json :seen_refs, null: false, default: {}  # ref → sha, as the last check saw them
      t.datetime :last_checked_at
      t.string :last_check_error
    end
    # One queued deploy per project: a newer push switches it to the newest SHA.
    add_index :deploys, :project_id, unique: true, where: "status = 'queued'", name: "index_deploys_one_queued"
  end
end
