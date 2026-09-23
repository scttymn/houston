# Build step 6, Batch 3: the data generation each deploy ran on. A snapshot
# is of the serving version, so it reads the running deploy's generation.
class AddGenerationToDeploys < ActiveRecord::Migration[8.1]
  def change
    add_column :deploys, :generation, :integer, default: 1, null: false
  end
end
