require "test_helper"
require_relative "../support/project_helpers"

class ProjectVolumeTest < ActiveSupport::TestCase
  include ProjectHelpers

  test "where a volume can live" do
    project = make_project("equip")
    project.update!(volumes: [ { "name" => "storage", "path" => "/rails/storage" } ])
    b2 = StorageLocation.create!(name: "b2-offsite", kind: "b2", settings: { "bucket" => "b" }, restic_password: "pw", verified_at: Time.current, acknowledged_at: Time.current)
    unfinished = StorageLocation.create!(name: "later", kind: "nfs", settings: { "server" => "10.0.1.21", "export" => "/x" }, restic_password: "pw", verified_at: Time.current)

    volume = ProjectVolume.choose!(project, "storage", storage_locations(:unas))
    assert_equal storage_locations(:unas), volume.location
    assert_equal volume, ProjectVolume.choose!(project, "storage", nil).tap { |v| assert_nil v.location }

    [ [ b2, "b2-offsite can't hold live volumes (backups only)" ], [ unfinished, "later isn't set up yet" ] ].each do |location, message|
      error = assert_raises(ProjectVolume::Refused) { ProjectVolume.choose!(project, "storage", location) }
      assert_equal message, error.message
    end
    assert_raises(ActiveRecord::RecordNotFound) { ProjectVolume.choose!(project, "nope", nil) }

    volume.update!(placed_at: Time.current)
    error = assert_raises(ProjectVolume::Placed) { ProjectVolume.choose!(project, "storage", storage_locations(:unas)) }
    assert_equal "storage is already on local disk; moving a volume is a later feature", error.message
  end
end
