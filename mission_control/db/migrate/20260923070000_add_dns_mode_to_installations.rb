class AddDnsModeToInstallations < ActiveRecord::Migration[8.1]
  def change
    # "wildcard": Houston owns *.<base>. "per_host": another server owns it
    # (migration), so Houston adds explicit records one name at a time.
    add_column :installations, :dns_mode, :string
  end
end
