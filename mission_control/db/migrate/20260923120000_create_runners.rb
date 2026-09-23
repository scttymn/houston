class CreateRunners < ActiveRecord::Migration[8.1]
  def change
    # houston-runner-1…n, as they poll for work.
    create_table :runners do |t|
      t.string :name, null: false
      t.datetime :last_seen_at, null: false
      t.timestamps
    end
    add_index :runners, :name, unique: true

    add_column :deploys, :runner, :string
  end
end
