# Build step 5, Batch 2: each project's keep rules (from sync) and each
# location's last prune.
class AddRetention < ActiveRecord::Migration[8.1]
  def change
    add_column :projects, :keep_auto, :integer, default: 14, null: false
    add_column :projects, :keep_deploy, :integer, default: 10, null: false
    add_column :storage_locations, :pruned_at, :datetime
    add_column :storage_locations, :prune_error, :string, limit: 2000
  end
end
