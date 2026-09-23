class CreateApiTokens < ActiveRecord::Migration[8.1]
  def change
    # Named personal tokens for the CLI (houston … --server) and agents.
    create_table :api_tokens do |t|
      t.string :name, null: false
      t.string :token_digest, null: false
      t.datetime :last_used_at
      t.timestamps
    end
    add_index :api_tokens, :name, unique: true
    add_index :api_tokens, :token_digest, unique: true
  end
end
