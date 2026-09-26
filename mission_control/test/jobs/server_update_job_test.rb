require "test_helper"
require_relative "../support/fake_docker"

# ServerUpdateJob, every minute: once the helper has ended, what happened is
# recorded, once (docs/plans/update-from-mission-control.md).
class ServerUpdateJobTest < ActiveJob::TestCase
  include FakeDockerHelper
  include Turbo::Broadcastable::TestHelper

  LOG = "==> Pulling Houston v0.4.3\n==> Starting Houston\n".freeze

  setup { ENV["HOUSTON_VERSION"] = "v0.4.3" }
  teardown { ENV.delete("HOUSTON_VERSION") }

  def running(started: 2.minutes.ago) = ServerUpdate.create!(to_version: "v0.4.3", from_version: "v0.4.2", status: "running", started_at: started)

  # state: the helper's "<status> <exit code>", or nil when it's gone.
  def helper(state, logs: LOG)
    FakeDocker.new do |args|
      case args.first
      when "inspect" then state ? DockerCommand::Result.new(success: true, output: "#{state}\n") : failure("Error: No such object: houston-update")
      when "logs" then DockerCommand::Result.new(success: true, output: logs)
      end
    end
  end

  def refreshes(&) = capture_turbo_stream_broadcasts(FlightBoard::STREAM, &).count { |s| s["action"] == "refresh" }

  test "each outcome is recorded once" do
    {
      "exited 0" => [ "go", LOG ],
      "exited 3" => [ "rolled_back", LOG ],
      "exited 1" => [ "no_go", LOG ],
      nil => [ "no_go", /helper \(houston-update\) is gone/ ]
    }.each do |state, (status, log)|
      update = running
      fake = helper(state)
      boards = refreshes { use_fake_docker(fake) { ServerUpdateJob.perform_now; ServerUpdateJob.perform_now } }
      update.reload
      assert_equal status, update.status, state.inspect
      assert_match log, update.log, state.inspect
      assert_in_delta Time.current, update.finished_at, 5
      assert_equal 1, boards, "#{state.inspect}: the board refreshes once"
      assert_includes fake.all_args, "--tail" if state
      assert_not ServerUpdate.running?
    end

    # Exited 0, but this Mission Control isn't the new version: not GO.
    ENV["HOUSTON_VERSION"] = "v0.4.2"
    update = running
    use_fake_docker(helper("exited 0")) { ServerUpdateJob.perform_now }
    assert_equal "no_go", update.reload.status
    assert_match "Mission Control runs v0.4.2", update.log
  end

  test "two checks at once record it once" do
    update = running
    # The other check writes its result while this one reads the helper's log.
    fake = FakeDocker.new do |args|
      case args.first
      when "inspect" then DockerCommand::Result.new(success: true, output: "exited 3\n")
      when "logs" then update.update_columns(status: "rolled_back", log: "the other check's", finished_at: Time.current); DockerCommand::Result.new(success: true, output: LOG)
      end
    end
    assert_equal 0, refreshes { use_fake_docker(fake) { ServerUpdateJob.perform_now } }
    assert_equal "the other check's", update.reload.log
  end

  test "a running update says what it's doing" do
    update = running
    logs = "==> houston update: installing v0.4.3\n==> Docker is installed\n==> Pulling Houston v0.4.3\n"
    boards = refreshes { use_fake_docker(helper("running 0", logs:)) { ServerUpdateJob.perform_now; ServerUpdateJob.perform_now } }
    assert_equal [ "Pulling Houston v0.4.3", 1 ], [ update.reload.step, boards ], "saved, and the board refreshed once"
    assert_equal logs, update.log, "the log so far, for Houston's page"
    more = logs + "Status: Downloaded newer image\n"
    assert_equal 0, refreshes { use_fake_docker(helper("running 0", logs: more)) { ServerUpdateJob.perform_now } }, "a longer log, the same step"
    assert_equal more, update.reload.log

    logs += "==> Writing /opt/houston/compose.yml\n==> Installing the houston CLI v0.4.3\n==> Starting Houston\n"
    use_fake_docker(helper("running 0", logs:)) { ServerUpdateJob.perform_now }
    assert_equal "Starting Houston", update.reload.step

    use_fake_docker(helper("running 0", logs: "==> houston update: installing v0.4.3\n")) { ServerUpdateJob.perform_now }
    assert_equal "Installing v0.4.3", update.reload.step
    logs = "==> houston update: v0.4.3 didn't install; putting v0.4.2 back\n"
    use_fake_docker(helper("running 0", logs:)) { ServerUpdateJob.perform_now }
    assert_equal "v0.4.3 didn't install; putting v0.4.2 back", update.reload.step

    fake = FakeDocker.new { |args| args.first == "inspect" ? DockerCommand::Result.new(success: true, output: "running 0\n") : failure("Cannot connect") }
    use_fake_docker(fake) { ServerUpdateJob.perform_now }
    assert_equal "v0.4.3 didn't install; putting v0.4.2 back", update.reload.step, "no log, no change"
  end

  test "while one runs, it's checked every 5 seconds" do
    running
    use_fake_docker(helper("running 0")) do
      assert_enqueued_with(job: ServerUpdateJob, args: [ true ]) { ServerUpdateJob.perform_now(true) }
      assert_no_enqueued_jobs { ServerUpdateJob.perform_now }
    end
    use_fake_docker(helper("exited 0")) { assert_no_enqueued_jobs { ServerUpdateJob.perform_now(true) } }
  end

  test "a running helper, or Docker not answering, changes nothing" do
    update = running
    use_fake_docker(helper("running 0")) { ServerUpdateJob.perform_now }
    assert_equal "running", update.reload.status

    use_fake_docker(FakeDocker.new { failure("Cannot connect to the Docker daemon") }) { ServerUpdateJob.perform_now }
    assert_equal "running", update.reload.status
    assert_nil update.finished_at
  end

  test "nothing running, nothing asked" do
    use_fake_docker(helper("exited 0")) { |fake| ServerUpdateJob.perform_now; assert_empty fake.calls }
  end

  test "a long update says so" do
    update = running(started: 21.minutes.ago)
    logs = StringIO.new
    old, Rails.logger = Rails.logger, ActiveSupport::Logger.new(logs)
    use_fake_docker(helper("running 0")) { ServerUpdateJob.perform_now }
    assert_match "the update to v0.4.3 has run for 21 minutes", logs.string
    assert update.reload.long?
    ServerUpdate.where(id: update.id).delete_all
    assert_not running.long?
  ensure
    Rails.logger = old if old
  end

  test "the update check is scheduled every minute" do
    job = YAML.load_file(Rails.root.join("config/recurring.yml")).dig("production", "settle_server_update")
    assert_equal [ "ServerUpdateJob", "every minute" ], job.values_at("class", "schedule")
  end
end
