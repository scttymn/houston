require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/backup_helpers"

class VolumePlacementTest < ActiveSupport::TestCase
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers

  INSPECT = [ "volume", "inspect", "--format", "{{json .Options}}" ].freeze

  setup do
    @project = make_project("equip")
    @project.update!(volumes: [ { "name" => "storage", "path" => "/rails/storage" }, { "name" => "media", "path" => "/media" }, { "name" => "cache", "path" => "/cache" } ])
    @nfs = storage_locations(:unas)
    @disk = StorageLocation.create!(name: "disk2", kind: "local", settings: { "path" => "/srv/houston" }, restic_password: "pw", verified_at: Time.current, acknowledged_at: Time.current)
    @project.project_volumes.create!(name: "media", location: @nfs)
    @project.project_volumes.create!(name: "cache", location: @disk)
  end

  def place(fake)
    use_fake_docker(fake) { VolumePlacement.new(@project).place! }
  end

  # The app's volumes don't exist yet; the location's own NFS volume (from setup) does.
  def missing = FakeDocker.new { |args| args[0..2] == [ "volume", "inspect", "--format" ] ? failure("Error response from daemon: no such volume\n") : nil }

  test "placing new volumes" do
    fake = missing
    place(fake)
    assert_equal [
      INSPECT + [ "equip_storage" ],
      [ "volume", "create", "equip_storage" ],
      INSPECT + [ "equip_media" ],
      # A missing named volume would be made empty on local disk by docker run -v: make sure it's the NFS one.
      [ "volume", "inspect", "houston-storage-unas-nfs" ],
      [ "run", "--rm", "--user", "0", "-v", "houston-storage-unas-nfs:/location", "--entrypoint", "mkdir", BackupHelpers::TOOLS, "-p", "/location/volumes/equip/media" ],
      [ "volume", "create", "--driver", "local", "--opt", "type=nfs", "--opt", "o=addr=10.0.1.20,rw,nfsvers=4", "--opt", "device=:/volume1/houston/volumes/equip/media", "equip_media" ],
      INSPECT + [ "equip_cache" ],
      [ "run", "--rm", "--user", "0", "-v", "/srv/houston:/location", "--entrypoint", "mkdir", BackupHelpers::TOOLS, "-p", "/location/volumes/equip/cache" ],
      [ "volume", "create", "--driver", "local", "--opt", "type=none", "--opt", "o=bind", "--opt", "device=/srv/houston/volumes/equip/cache", "equip_cache" ]
    ], fake.calls.map(&:args)
    assert_equal %w[cache media storage], @project.project_volumes.where.not(placed_at: nil).order(:name).pluck(:name)
    assert_nil @project.project_volumes.find_by!(name: "storage").location, "local disk unless chosen"
  end

  test "a volume that's already somewhere" do
    already = FakeDocker.new do |args|
      case args.last
      when "equip_storage" then DockerCommand::Result.new(success: true, output: "null\n")
      when "equip_media" then DockerCommand::Result.new(success: true, output: '{"device":":/volume1/houston/volumes/equip/media","o":"addr=10.0.1.20,rw,nfsvers=4","type":"nfs"}' + "\n")
      when "equip_cache" then DockerCommand::Result.new(success: true, output: '{"device":"/srv/houston/volumes/equip/cache","o":"bind","type":"none"}' + "\n")
      end
    end
    place(already)
    assert_equal 3, already.calls.size, "inspected, nothing created"
    assert_equal 3, @project.project_volumes.where.not(placed_at: nil).count

    # Chosen on NFS, but it's on local disk: refused, nothing created.
    @project.project_volumes.update_all(placed_at: nil)
    plain = FakeDocker.new { |args| args[0..1] == %w[volume inspect] ? DockerCommand::Result.new(success: true, output: "null\n") : nil }
    error = assert_raises(VolumePlacement::Refused) { place(plain) }
    assert_equal "equip_media already exists on local disk, not unas-nfs; Houston doesn't move volumes yet: choose local disk, or move it yourself", error.message
    assert_not plain.calls.any? { |c| c.args[0..1] == %w[volume create] }

    # mkdir failing (the export unreachable): refused with its words.
    failing = FakeDocker.new do |args|
      next failure("no such volume\n") if args[0..2] == [ "volume", "inspect", "--format" ]
      failure("mount.nfs: Connection timed out\n") if args.include?("mkdir")
    end
    error = assert_raises(VolumePlacement::Refused) { place(failing) }
    assert_match "couldn't make equip_media's directory on unas-nfs: mount.nfs: Connection timed out", error.message
  end

  test "placing generation 2's volumes" do
    fake = missing
    use_fake_docker(fake) { VolumePlacement.new(@project, generation: 2).place! }
    calls = fake.calls.map(&:args)
    assert_includes calls, [ "volume", "create", "equip.g2_storage" ]
    assert_includes calls, [ "run", "--rm", "--user", "0", "-v", "houston-storage-unas-nfs:/location", "--entrypoint", "mkdir", BackupHelpers::TOOLS, "-p", "/location/volumes/equip.g2/media" ]
    assert_includes calls, [ "volume", "create", "--driver", "local", "--opt", "type=nfs", "--opt", "o=addr=10.0.1.20,rw,nfsvers=4",
                             "--opt", "device=:/volume1/houston/volumes/equip.g2/media", "equip.g2_media" ]
    assert_not calls.any? { |a| a.last == "equip_storage" }, "generation 1's volumes untouched"
  end
end
