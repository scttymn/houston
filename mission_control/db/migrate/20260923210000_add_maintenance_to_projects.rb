# Build step 6, Batch 1: Houston's maintenance page, the admin's switch.
class AddMaintenanceToProjects < ActiveRecord::Migration[8.1]
  def change
    add_column :projects, :maintenance_since, :datetime
    add_column :projects, :maintenance_by, :string
    add_column :projects, :maintenance_message, :string, limit: 500
  end
end
