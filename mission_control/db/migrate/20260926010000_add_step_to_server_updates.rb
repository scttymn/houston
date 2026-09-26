# What a running server update is doing: the installer's latest step.
class AddStepToServerUpdates < ActiveRecord::Migration[8.1]
  def change
    add_column :server_updates, :step, :string
  end
end
