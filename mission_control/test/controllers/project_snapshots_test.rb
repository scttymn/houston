require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/backup_helpers"

class ProjectSnapshotsTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers

  setup { @project = make_backup_project }

  def listing(*snapshots) = FakeDocker.new { DockerCommand::Result.new(success: true, output: snapshots.to_json) }

  test "the snapshots panel" do
    sign_in_as users(:one)
    fake = listing(snapshot_json(id: "11111111", time: "2026-09-21T03:00:00Z"),
                   snapshot_json(id: "22222222", time: "2026-09-21T11:20:00Z", reason: "manual", bytes: 404_000_000),
                   snapshot_json(id: "33333333", time: "2026-09-22T12:31:00Z", kind: "deploy", reason: "deploy", deploy: 7, sha: "d4e0b17" + "0" * 33))
    use_fake_docker(fake) do
      get project_snapshots_path("equip")
      assert_response :success
      assert_select "turbo-frame#snapshots"
      assert_select "[data-snapshot]", 2
      assert_select "[data-snapshot]:first-child", /Back up now.*385 MB/m
      assert_select ".snapshots", %r{kept 2 / 14}
      assert_select "a[href=?]", "/projects/equip/snapshots?kind=deploy", text: "Pre-deploy"

      get project_snapshots_path("equip", kind: "deploy")
      assert_select "[data-snapshot]", 1
      assert_select "[data-snapshot]", /before deploy #7.*d4e0b17/m
      assert_select ".snapshots", %r{kept 1 / 10}
    end
    assert_equal 1, fake.calls.size, "one listing serves both tabs"
  end

  test "the panel says why it has nothing" do
    sign_in_as users(:one)
    use_fake_docker(FakeDocker.new { failure("Fatal: unable to open repository at /repo: permission denied\n") }) do
      get project_snapshots_path("equip")
      assert_response :success
      assert_select ".snapshots", /Can't read snapshots.*unable to open repository/m
    end

    # No acknowledged storage: setup isn't finished, so its gate answers first.
    StorageLocation.update_all(acknowledged_at: nil)
    fake = FakeDocker.new
    use_fake_docker(fake) { get project_snapshots_path("equip") }
    assert_redirected_to setup_storage_path
    assert_empty fake.calls
    StorageLocation.update_all(acknowledged_at: Time.current)

    get project_snapshots_path("nope")
    assert_response :not_found
  end

  test "snapshots need the admin" do
    fake = listing
    use_fake_docker(fake) { get project_snapshots_path("equip") }
    assert_redirected_to new_session_path
    assert_empty fake.calls
  end
end
