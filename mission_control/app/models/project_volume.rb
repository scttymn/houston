# Where one of a project's named volumes lives: local disk (no location) or
# a location that holds live volumes (nfs, a host path). Chosen until Houston
# makes the volume (placed_at); fixed after, since moving is a later feature.
class ProjectVolume < ApplicationRecord
  class Refused < StandardError; end
  class Placed < StandardError; end

  belongs_to :project
  belongs_to :location, class_name: "StorageLocation", optional: true

  # Chooses where project's volume name lives (nil: local disk). Raises
  # RecordNotFound for a volume the compose file doesn't have.
  def self.choose!(project, name, location)
    raise ActiveRecord::RecordNotFound, "#{project.name} has no volume #{name}" unless project.volumes.any? { |v| v["name"] == name }
    if location
      raise Refused, "#{location.name} can't hold live volumes (backups only)" unless location.live?
      raise Refused, "#{location.name} isn't set up yet" unless location.acknowledged?
    end

    volume = project.project_volumes.create_or_find_by!(name:)
    raise Placed, "#{name} is already on #{volume.where_words}; moving a volume is a later feature" if volume.placed_at
    volume.update!(location:)
    volume
  end

  def where_words = location&.name || "local disk"
end
