# Port 3000, open to the network or not (Settings › Port 3000). Every
# install so far has it open, as does a new one (setup needs it).
class AddPortOpenToInstallations < ActiveRecord::Migration[8.1]
  def change
    add_column :installations, :port_open, :boolean, null: false, default: true
  end
end
