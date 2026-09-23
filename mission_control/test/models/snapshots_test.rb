require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/backup_helpers"

class SnapshotsTest < ActiveSupport::TestCase
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers

  setup do
    @project = make_backup_project
    @location = storage_locations(:unas)
  end

  test "listing a project's snapshots" do
    listed = [ snapshot_json(id: "11111111", time: "2026-09-20T03:00:01.5Z"),
               snapshot_json(id: "33333333", time: "2026-09-22T12:31:00Z", kind: "deploy", reason: "deploy", deploy: 7, bytes: 5),
               snapshot_json(id: "22222222", time: "2026-09-21T11:20:00Z", reason: "manual") ]
    fake = FakeDocker.new { DockerCommand::Result.new(success: true, output: listed.to_json) }

    snapshots = use_fake_docker(fake) { Snapshots.for(@project, @location) }

    assert_equal [ StorageLocation::RESTIC_IMAGE, "snapshots", "--json", "--host", "houston", "--tag", "project:equip" ],
                 fake.calls.first.args.drop(fake.calls.first.args.index(StorageLocation::RESTIC_IMAGE))
    assert_equal "restic-password-xyz", fake.calls.first.env["RESTIC_PASSWORD"]
    assert_equal %w[33333333 22222222 11111111], snapshots.map(&:short_id)
    newest = snapshots.first
    assert_equal [ "deploy", "deploy", 7, "a" * 40, 5, Time.utc(2026, 9, 22, 12, 31) ], [ newest.kind, newest.reason, newest.deploy, newest.sha, newest.bytes, newest.time ]
    assert_equal "33333333".ljust(64, "0"), newest.id
    assert_nil snapshots.last.deploy

    # Cached: no second restic run.
    use_fake_docker(fake) { Snapshots.for(@project, @location) }
    assert_equal 1, fake.calls.size

    # Past the limit, the newest are kept.
    Rails.cache.clear
    many = Array.new(3) { |i| snapshot_json(id: "#{i}" * 8, time: "2026-09-2#{i}T03:00:00Z") }
    stub_const_limit(2) do
      kept = use_fake_docker(FakeDocker.new { DockerCommand::Result.new(success: true, output: many.to_json) }) { Snapshots.for(@project, @location) }
      assert_equal %w[22222222 11111111], kept.map(&:short_id)
    end
  end

  def stub_const_limit(limit)
    original = Snapshots::LIMIT
    Snapshots.send(:remove_const, :LIMIT)
    Snapshots.const_set(:LIMIT, limit)
    yield
  ensure
    Snapshots.send(:remove_const, :LIMIT)
    Snapshots.const_set(:LIMIT, original)
  end

  test "restic failing isn't cached" do
    failing = FakeDocker.new { failure("Fatal: unable to open repository at /repo: permission denied\n") }
    error = assert_raises(Snapshots::Unavailable) { use_fake_docker(failing) { Snapshots.for(@project, @location) } }
    assert_match "unable to open repository", error.message

    working = FakeDocker.new { DockerCommand::Result.new(success: true, output: "[]") }
    assert_equal [], use_fake_docker(working) { Snapshots.for(@project, @location) }
    assert_equal 1, working.calls.size

    garbage = FakeDocker.new { DockerCommand::Result.new(success: true, output: "not json") }
    Rails.cache.clear
    assert_raises(Snapshots::Unavailable) { use_fake_docker(garbage) { Snapshots.for(@project, @location) } }
  end
end
