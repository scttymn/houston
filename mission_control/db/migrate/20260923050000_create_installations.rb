class CreateInstallations < ActiveRecord::Migration[8.1]
  def change
    create_table :installations do |t|
      t.string :base_domain
      t.string :cloudflare_account_id
      t.string :cloudflare_zone_id
      t.string :tunnel_id
      t.text :cloudflare_api_token
      t.text :tunnel_token
      t.datetime :cloudflare_connected_at
      t.timestamps
    end
  end
end
