# Sessions end, and remember whether they were made through the tunnel.
# Sessions made before this can't say, so they count as made on the network:
# signing in again through admin.<base> makes a tunnel one.
class AddTunnelAndActivityToSessions < ActiveRecord::Migration[8.1]
  def change
    add_column :sessions, :tunnel, :boolean, null: false, default: false
    add_column :sessions, :last_active_at, :datetime
  end
end
