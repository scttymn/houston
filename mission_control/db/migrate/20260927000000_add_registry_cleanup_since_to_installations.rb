# Garbage collection of Houston's registry after a project is deleted
# (docs/plans/delete-project.md, Batch 7): set while it runs, so no image is
# pushed meanwhile.
class AddRegistryCleanupSinceToInstallations < ActiveRecord::Migration[8.1]
  def change
    add_column :installations, :registry_cleanup_since, :datetime
  end
end
