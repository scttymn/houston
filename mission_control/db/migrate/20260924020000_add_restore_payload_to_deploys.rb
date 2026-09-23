# Build step 6, Batch 6 (review rounds 2 and 3): a restore keeps its
# snapshot's compose.yml, as its check sync sent it, until the flip applies
# it; switched_at marks the restore whose version kamal-proxy switched to.
class AddRestorePayloadToDeploys < ActiveRecord::Migration[8.1]
  def change
    add_column :deploys, :sync_payload, :json
    add_column :deploys, :switched_at, :datetime
  end
end
