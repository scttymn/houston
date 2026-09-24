require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/backup_helpers"
require_relative "../support/api_helpers"

# The flight board redraws itself when something it shows changes
# (docs/plans/live-flight-board.md): Mission Control sends its stream a
# refresh, and each open board re-fetches and morphs in place.
class FlightBoardTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include BackupHelpers
  include ApiHelpers

  def refreshes(&) = capture_turbo_stream_broadcasts(FlightBoard::STREAM, &).count { |s| s["action"] == "refresh" }

  test "a queued deploy refreshes the board" do
    project = make_project("equip")
    assert_equal 1, refreshes { Deploy.queue!(project, sha: "a" * 40, ref: "refs/heads/main") }
    assert_equal 1, refreshes { Deploy.queue!(project, sha: "b" * 40, ref: "refs/heads/main") }, "a newer push switches the queued deploy"
  end

  test "a deploy started from the CLI refreshes the board" do
    project = make_project("equip")
    assert_equal 1, refreshes { Deploy.start!(project, sha: "a" * 40, ref: "refs/heads/main") }, "created in flight: the status column's default, so no status change"
  end

  test "step and status refresh; log chunks don't" do
    project = make_project("equip")
    Deploy.queue!(project, sha: "a" * 40, ref: "refs/heads/main")
    assert_equal 1, refreshes { Deploy.claim_next!(runner: "houston-runner-1") }, "a runner's claim"
    deploy = project.deploys.sole
    token = Deploy.claim!(deploy, runner: "x") # already in flight: nothing claimed, nothing sent
    assert_nil token

    started, token, = Deploy.start!(make_project("garage"), sha: "c" * 40, ref: "refs/heads/main")
    report = ->(body) { patch "/api/deploys/#{started.id}", params: body.to_json, headers: api_headers.merge("X-Houston-Deploy-Token" => token) }
    assert_equal 0, refreshes { report.({ log: "building\n" }) }, "a log chunk (and its heartbeat)"
    assert_equal 1, refreshes { report.({ step: "Build" }) }
    assert_equal 0, refreshes { report.({ step: "Build", log: "more\n" }) }, "the same step again"
    assert_equal 1, refreshes { report.({ status: "go" }) }
  end

  test "backup runs refresh on status changes" do
    project = make_backup_project
    run = nil
    assert_equal 1, refreshes { run = BackupRun.request!(project) }
    token = nil
    assert_equal 1, refreshes { token = BackupRun.claim!(run) }
    assert_equal 0, refreshes { run.heartbeat!(token) }
    assert_equal 1, refreshes { run.finish!(token, status: "go", snapshot_id: "a" * 64, bytes: 1) }

    queued = BackupRun.request!(project)
    assert_equal 1, refreshes { queued.give_up!("no runner came") }
  end

  test "projects and maintenance refresh the board" do
    project = nil
    assert_equal 1, refreshes { project = make_project("equip") }
    assert_equal 1, refreshes { project.update!(maintenance_since: Time.current, maintenance_by: "me") }
    assert_equal 0, refreshes { project.update!(last_checked_at: Time.current) }, "a change the board doesn't show"
  end

  test "a failed refresh never fails the change" do
    project = make_project("equip")
    original = Turbo::StreamsChannel.method(:broadcast_refresh_to)
    Turbo::StreamsChannel.define_singleton_method(:broadcast_refresh_to) { |*| raise "cable down" }
    assert_nothing_raised { Deploy.queue!(project, sha: "a" * 40, ref: "refs/heads/main") }
    assert_equal "queued", project.deploys.sole.status
  ensure
    Turbo::StreamsChannel.define_singleton_method(:broadcast_refresh_to, original)
  end
end
