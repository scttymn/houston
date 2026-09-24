# What the project page shows beyond what Houston acts on: services' images,
# the app's limits, the console command (the sync's details).
class AddDetailsToProjects < ActiveRecord::Migration[8.1]
  def change
    add_column :projects, :details, :json, default: {}, null: false
  end
end
