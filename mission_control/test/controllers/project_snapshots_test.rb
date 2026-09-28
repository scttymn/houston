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
      assert_select "turbo-frame#snapshots-list"
      assert_select "[data-snapshot]", 2
      assert_match(/created by hand.*385 MB/m, css_select("[data-snapshot]").first.text)
      assert_select "#snapshots-list", %r{kept 2 / 14}
      assert_select "#snapshots-list", %r{Daily at 03:00 \(UTC\), plus any you create}
      assert_select "a[href=?]", "/projects/equip/snapshots?kind=deploy", text: /Pre-deploy/

      get project_snapshots_path("equip", kind: "deploy")
      assert_select "[data-snapshot]", 1
      assert_select "[data-snapshot]", /before deploy #7.*d4e0b17/m
      assert_select "#snapshots-list", %r{kept 1 / 10}
    end
    assert_equal 1, fake.calls.size, "one listing serves both tabs"
  end

  # A deleted project's final snapshot (docs/plans/delete-project.md), seen
  # once a project of its name is added again: on the Pre-deploy tab, which
  # keeps the snapshots taken before a change, and never counted against it.
  test "a deleted project's final snapshot" do
    sign_in_as users(:one)
    fake = listing(snapshot_json(id: "33333333", time: "2026-09-22T12:31:00Z", kind: "deploy", reason: "deploy", deploy: 7),
                   snapshot_json(id: "44444444", time: "2026-09-23T09:00:00Z", kind: "final", reason: "delete", sha: "e1e7ed0" + "0" * 33))
    use_fake_docker(fake) { get project_snapshots_path("equip", kind: "deploy") }
    assert_select "[data-snapshot]", 2
    assert_select "[data-snapshot='44444444']", /before it was deleted.*e1e7ed0/m
    assert_select "[data-snapshot='44444444'] a", "Restore"
    assert_select "#snapshots-list", %r{kept 1 / 10}
  end

  # (A project always has storage once setup is finished: its gate needs a default.)
  test "tabs by kind; the counts are in each tab's note" do
    sign_in_as users(:one)
    fake = listing(snapshot_json(id: "11111111", time: "2026-09-21T03:00:00Z"),
                   snapshot_json(id: "33333333", time: "2026-09-22T12:31:00Z", kind: "deploy", reason: "deploy", deploy: 7))
    use_fake_docker(fake) { get project_snapshots_path("equip") }
    assert_select "#snapshots-list [role=tablist] a", 3
    assert_select "#snapshots-list [role=tablist] a:last-child", "Settings"
    assert_select "#snapshots-list [role=tablist] a[aria-selected=true]", "Scheduled"
    assert_select "#snapshots-list [role=tablist]", { text: %r{/}, count: 0 }, "no counts in the tabs: they pushed the tabs off a phone's screen; the notes say them"
    assert_select "#snapshots-list", %r{kept 1 / 14}
    assert_select "#snapshots-list [role=tablist] a[aria-selected=false]", "Pre-deploy"
  end

  test "the panel says why it has nothing" do
    sign_in_as users(:one)
    use_fake_docker(FakeDocker.new { failure("Fatal: unable to open repository at /repo: permission denied\n") }) do
      get project_snapshots_path("equip")
      assert_response :success
      assert_select "#snapshots-list", /Can't read snapshots.*unable to open repository/m
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
    assert_redirected_to sign_in_path
    assert_empty fake.calls
  end

  # Downloading a snapshot (docs/plans/download-snapshot.md, Batch 1).
  def export_docker(download: [ "PK\x03\x04abc".b, "def" ], created: nil)
    FakeDocker.new(download:) do |args, _env|
      if args.include?("snapshots")
        DockerCommand::Result.new(success: true, output: [ snapshot_json(id: "11111111", time: "2026-09-21T03:00:00Z") ].to_json)
      elsif args.first == "inspect"
        created ? DockerCommand::Result.new(success: true, output: "c0ffee #{created.utc.iso8601} someone-else\n") : failure("Error: No such object\n")
      end
    end
  end

  test "downloading a snapshot" do
    sign_in_as users(:one)
    fake = export_docker
    use_fake_docker(fake) { get download_project_snapshot_path("equip", "11111111", location: "unas-nfs") }

    assert_response :success
    assert_equal "application/zip", response.media_type
    assert_equal "attachment; filename=\"equip-20260921-0300Z-11111111.zip\"; filename*=UTF-8''equip-20260921-0300Z-11111111.zip", response.headers["Content-Disposition"]
    assert_includes response.headers["Cache-Control"], "no-store"
    # Not read whole to be digested (a body with to_ary would be: Rack::ETag,
    # and no-store doesn't stop it). The test harness itself buffers, so its
    # Content-Length says nothing; the real run streams with curl.
    assert_nil response.headers["ETag"]
    assert_equal "PK\x03\x04abcdef".b, response.body.b
    assert fake.calls.one? { |c| c.args.include?("dump") }
  end

  test "a download that's refused" do
    fake = export_docker
    use_fake_docker(fake) { get download_project_snapshot_path("equip", "11111111", location: "unas-nfs") }
    assert_redirected_to sign_in_path
    assert_empty fake.calls

    sign_in_as users(:one)
    [ [ "99999999", "unas-nfs" ], [ "11111111", "nowhere" ] ].each do |snapshot, location|
      fake = export_docker
      use_fake_docker(fake) { get download_project_snapshot_path("equip", snapshot, location:) }
      assert_response :not_found
      assert fake.calls.none? { |c| c.args.include?("dump") }
    end
    get download_project_snapshot_path("nope", "11111111", location: "unas-nfs")
    assert_response :not_found

    in_use = failure("docker: Error response from daemon: Conflict. The container name \"/houston-export\" is already in use\n", code: 125)
    use_fake_docker(export_docker(download: in_use, created: 2.minutes.ago)) { get download_project_snapshot_path("equip", "11111111", location: "unas-nfs") }
    assert_redirected_to project_path("equip")
    assert_match(/Can't download snapshot 11111111: another download started at/, flash[:alert])

    use_fake_docker(export_docker(download: failure("Fatal: wrong password or no key found\n", code: 12))) { get download_project_snapshot_path("equip", "11111111", location: "unas-nfs") }
    assert_redirected_to project_path("equip")
    assert_match(/Can't download snapshot 11111111: .*wrong password/, flash[:alert])

    # Any id, when the listing fails: the alert stays small enough for the flash cookie.
    Rails.cache.clear
    use_fake_docker(FakeDocker.new { failure("Fatal: unable to open repository\n") }) do
      get download_project_snapshot_path("equip", "a" * 5000, location: "unas-nfs")
    end
    assert_redirected_to project_path("equip")
    assert_operator flash[:alert].size, :<, 1000
  end

  test "each snapshot links its download" do
    sign_in_as users(:one)
    use_fake_docker(export_docker) { get project_snapshots_path("equip") }

    link = css_select("[data-snapshot='11111111'] a[href='/projects/equip/snapshots/11111111/download?location=unas-nfs']")
    assert_equal 1, link.size
    assert_equal "Download", link.first.text
    assert_equal "false", link.first["data-turbo"]
    assert_match(/Download everything in this snapshot, about 391 MB/, link.first["title"])
    assert_select "a[download]", 0, "a refusal redirects; with download, the browser would save the page as the file"
    assert_select "#snapshots-list", %r{Download gives its files \(data/\) and databases \(out/, with houston.json saying which is which\)}
  end
end
