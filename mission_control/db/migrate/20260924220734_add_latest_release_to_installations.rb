class AddLatestReleaseToInstallations < ActiveRecord::Migration[8.1]
  def change
    add_column :installations, :latest_release, :string
    add_column :installations, :latest_release_url, :string
    add_column :installations, :latest_release_checked_at, :datetime
  end
end
