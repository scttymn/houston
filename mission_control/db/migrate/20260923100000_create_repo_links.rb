class CreateRepoLinks < ActiveRecord::Migration[8.1]
  def change
    # A repo being linked on the Add project page, until Save.
    create_table :repo_links do |t|
      t.string :repo_url, null: false
      t.string :branch, null: false, default: "main"
      t.string :compose_path, null: false, default: "compose.yml"
      t.text :deploy_key_private, null: false
      t.string :deploy_key_public, null: false
      t.json :preview
      t.string :preview_sha
      t.timestamps
    end

    change_table :projects do |t|
      t.string :repo_url
      t.string :branch
      t.string :compose_path
      t.text :deploy_key_private
      t.string :deploy_key_public
      t.text :webhook_secret
      t.datetime :webhook_verified_at
    end
  end
end
