# A rebuild: a deploy built without Docker's layer cache (docs/plans/rebuild.md).
class AddFreshToDeploys < ActiveRecord::Migration[8.1]
  def change
    add_column :deploys, :fresh, :boolean, default: false, null: false
  end
end
