# Build step 6, Batch 2: a project's own maintenance page, from its repo.
class AddMaintenancePageToProjects < ActiveRecord::Migration[8.1]
  def change
    add_column :projects, :maintenance_page, :text
  end
end
