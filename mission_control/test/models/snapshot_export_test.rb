require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/backup_helpers"

# Downloading a snapshot (docs/plans/download-snapshot.md, Batch 1).
class SnapshotExportTest < ActiveSupport::TestCase
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers

  ZIP = "PK\x03\x04".b

  setup do
    @project = make_backup_project
    @location = storage_locations(:unas)
  end

  # docker as the server answers: the project's listing, and inspect giving
  # the start of any houston-export container (created) or none.
  # docker as the server answers: the project's listing, and inspect giving
  # the container holding the name (its id, start and download label; by
  # default this download's own, read from its run), or none.
  def docker(download: [ ZIP + "abc", "def" ], listing: [ snapshot_json(id: "5c5edd4c", time: "2026-09-21T03:00:00Z") ], created: nil, holder: nil)
    fake = FakeDocker.new(download:) do |args, _env|
      if args.include?("snapshots")
        DockerCommand::Result.new(success: true, output: listing.to_json)
      elsif args.first == "inspect"
        label = holder || fake.calls.reverse.find { |c| c.args.include?("dump") }&.args&.then { |a| a[a.index("--label") + 1].split("=", 2).last }
        (created || label) ? DockerCommand::Result.new(success: true, output: "c0ffee #{(created || Time.current).utc.iso8601(9)} #{label}\n") : failure("Error: No such object: houston-export\n")
      end
    end
  end

  def open_export(fake, location: "unas-nfs", snapshot: "5c5edd4c")
    use_fake_docker(fake) { SnapshotExport.open(@project, location_name: location, snapshot:, by: "admin@example.com") }
  end

  def dumps(fake) = fake.calls.select { |c| c.args.include?("dump") }
  def removals(fake) = fake.calls.select { |c| c.args.first(2) == %w[rm -f] }

  def logged
    io = StringIO.new
    original = Rails.logger
    Rails.logger = ActiveSupport::Logger.new(io)
    yield
    io.string
  ensure
    Rails.logger = original
  end

  test "the download's command" do
    fake = docker
    export = open_export(fake)

    assert_equal 1, dumps(fake).size
    call = dumps(fake).first
    assert_equal %w[run --rm --name houston-export], call.args.first(4)
    assert_match(/\Ahouston.export=\h{32}\z/, call.args[call.args.index("--label") + 1], "each download labels its container, to know it's its own")
    assert_equal [ StorageLocation::RESTIC_IMAGE, "dump", "--quiet", "--retry-lock", "30s", "--archive", "zip", "5c5edd4c".ljust(64, "0"), "/" ],
                 call.args.drop(call.args.index(StorageLocation::RESTIC_IMAGE)), "a short id is sent as the listing's full id"
    assert_includes call.args, "#{@location.volume_name}:/repo"
    assert_equal 3.hours.to_i, call.timeout
    assert_equal "restic-password-xyz", call.env["RESTIC_PASSWORD"]
    assert_not_includes call.args.join(" "), "restic-password-xyz"
    assert_equal "equip-20260921-0300Z-5c5edd4c.zip", export.filename

    full = open_export(docker, snapshot: "5c5edd4c".ljust(64, "0"))
    assert_equal "equip-20260921-0300Z-5c5edd4c.zip", full.filename, "the full id finds it too"
  end

  test "only the project's own snapshots" do
    # A location the project never backed up to, or none of that name.
    StorageLocation.create!(name: "b2-offsite", kind: "b2", settings: { "bucket" => "b" }, restic_password: "p", verified_at: Time.current, acknowledged_at: Time.current)
    [ "b2-offsite", "nowhere", "" ].each do |location|
      fake = docker
      assert_raises(SnapshotExport::NotFound, location) { open_export(fake, location:) }
      assert_empty dumps(fake), location
    end

    # The listing is the project's own (restic filters by its tag), so another
    # project's snapshot in the same repository isn't found. Nor a prefix, or nothing.
    [ "99999999", "5c5e", "" ].each do |snapshot|
      fake = docker
      error = assert_raises(SnapshotExport::NotFound, snapshot) { open_export(fake, snapshot:) }
      assert_empty dumps(fake), snapshot
      assert_match(/isn't in unas-nfs's snapshots of equip/, error.message) if snapshot.present?
    end

    Rails.cache.clear
    fake = FakeDocker.new { failure("Fatal: unable to open repository at /repo: permission denied\n") }
    error = assert_raises(SnapshotExport::Failed) { open_export(fake) }
    assert_match(/can't read unas-nfs's snapshots: .*permission denied/, error.message)
    assert_empty dumps(fake)
  end

  test "a download that can't start" do
    fake = docker(download: failure("Fatal: wrong password or no key found\n", code: 12))
    error = assert_raises(SnapshotExport::Failed) { open_export(fake) }
    assert_match(/exit 12.*wrong password/, error.message)
    assert_empty removals(fake)

    error = assert_raises(SnapshotExport::Failed) { open_export(docker(download: failure("unable to create lock in backend: repository is already locked exclusively\n", code: 11))) }
    assert_match(/the repository is busy \(pruning\?\); try again in a few minutes/, error.message)

    # Whatever restic prints, only a zip is ever sent.
    fake = docker(download: [ "repo already locked, waiting up to 30s for the lock\n", ZIP ])
    error = assert_raises(SnapshotExport::Failed) { open_export(fake) }
    assert_match(/restic didn't send a zip: repo already locked/, error.message)
    assert fake.downloads.first.closed?
    assert_equal 1, removals(fake).size

    # Another download holds the name, and it's young: busy, and it's left alone.
    in_use = failure("docker: Error response from daemon: Conflict. The container name \"/houston-export\" is already in use by container \"dd06\".\n", code: 125)
    fake = docker(download: in_use, created: 5.minutes.ago)
    error = assert_raises(SnapshotExport::Busy) { open_export(fake) }
    assert_match(/another download started at \d\d:\d\d .* is still running/, error.message)
    assert_empty removals(fake)
    assert_equal 1, dumps(fake).size

    # Older than the deadline: removed, and the download tried once more.
    attempts = 0
    fake = docker(download: ->(_) { (attempts += 1) == 1 ? in_use : [ ZIP + "abc" ] }, created: 4.hours.ago)
    export = open_export(fake)
    assert_equal [ %w[rm -f c0ffee] ], removals(fake).map(&:args), "by the id it inspected: by name, it could hit a download that just took the name"
    assert_equal 2, dumps(fake).size
    assert_equal ZIP + "abc", export.to_enum(:each).to_a.join

    # Taken again by the time it tries: busy, no third try.
    fake = docker(download: in_use, created: 4.hours.ago)
    assert_raises(SnapshotExport::Busy) { open_export(fake) }
    assert_equal 2, dumps(fake).size
  end

  test "a download's body and its log" do
    fake = docker(download: [ ZIP + "a", "bc", "d" ])
    log = logged do
      export = open_export(fake)
      assert_equal ZIP + "abcd", export.to_enum(:each).to_a.join
      export.close
    end
    assert_includes log, "download: admin@example.com equip 5c5edd4c from unas-nfs: started"
    assert_includes log, "download: admin@example.com equip 5c5edd4c from unas-nfs: finished, 8 bytes"
    assert_empty removals(fake), "it ended by itself"

    fake = docker(download: { chunks: [ ZIP + "a" ], broken: "exit 1: Fatal: pack 1b08 not found" })
    log = logged do
      export = open_export(fake)
      assert_raises(DockerCommand::Broken) { export.each { } }
      export.close
    end
    assert_includes log, "equip 5c5edd4c from unas-nfs: broke after 5 bytes: exit 1: Fatal: pack 1b08 not found"
    assert_empty removals(fake)

    # The client goes away after the first chunk.
    fake = docker(download: [ ZIP + "a", "bc", "d" ])
    log = logged do
      use_fake_docker(fake) do
        export = open_export(fake)
        export.each { break }
        export.close
      end
    end
    assert_includes log, "equip 5c5edd4c from unas-nfs: stopped by the client after 5 bytes"
    assert fake.downloads.first.closed?
    assert_equal [ %w[rm -f c0ffee] ], removals(fake).map(&:args)
    assert_not_includes log, "restic-password-xyz"

    # Its container already gone, and another download holds the name: that one's left alone.
    fake = docker(download: [ ZIP + "a", "bc" ], holder: "someone-else")
    use_fake_docker(fake) do
      export = open_export(fake)
      export.each { break }
      export.close
    end
    assert_empty removals(fake)
  end
end
