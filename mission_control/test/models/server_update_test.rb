require "test_helper"
require_relative "../support/fake_docker"
require_relative "../support/project_helpers"

# Updating this server from Mission Control
# (docs/plans/update-from-mission-control.md): a helper container runs the
# release's installer on the host, and puts the old version back if it fails.
class ServerUpdateTest < ActiveSupport::TestCase
  include ActiveJob::TestHelper
  include FakeDockerHelper
  include ProjectHelpers

  RUNNER = "ghcr.io/scttymn/houston-runner:v0.4.2@sha256:#{"a" * 64}".freeze
  LABELS = %({"com.docker.compose.project":"houston","com.docker.compose.project.config_files":"/srv/houston/compose.yml","com.docker.compose.project.working_dir":"/srv/houston"}).freeze

  setup { defaults! }
  teardown { %w[HOUSTON_VERSION HOUSTON_SOURCE_SHA HOUSTON_RUNNER_IMAGE HOUSTON_RUNNERS].each { ENV.delete(it) } }

  # A v0.4.2 server, installed by the installer, with v0.4.3 out.
  def defaults!
    ENV["HOUSTON_VERSION"] = "v0.4.2"
    ENV.delete("HOUSTON_SOURCE_SHA")
    ENV["HOUSTON_RUNNER_IMAGE"] = RUNNER
    ENV["HOUSTON_RUNNERS"] = "3"
    Installation.current.update!(latest_release: "v0.4.3", latest_release_url: "https://github.com/scttymn/houston/releases/tag/v0.4.3")
  end

  # labels: this container's; run: whether the helper starts; on_run: called
  # as it does (a claim landing meanwhile).
  def docker(labels: LABELS, run: true, on_run: nil)
    FakeDocker.new do |args|
      case args.first
      when "inspect" then DockerCommand::Result.new(success: true, output: labels)
      when "run"
        on_run&.call
        failure("docker: Error response from daemon: Conflict. The container name \"/houston-update\" is already in use") unless run
      end
    end
  end

  def helper_runs(fake) = fake.calls.select { |c| c.args.first == "run" }

  test "start runs the helper for that release" do
    update = nil
    use_fake_docker(docker) do |fake|
      update = ServerUpdate.start!
      assert_equal [ "rm", "houston-update" ], fake.calls.find { |c| c.args.first == "rm" }&.args, "a finished helper is removed first"
      assert_equal [ "run", "-d", "--name", "houston-update", "--privileged", "--pid=host", "--user", "0",
                     "-e", "HOUSTON_UPDATE_TO=v0.4.3", "-e", "HOUSTON_UPDATE_FROM=v0.4.2", "-e", "HOUSTON_REPO=scttymn/houston",
                     "-e", "HOUSTON_DIR=/srv/houston", "-e", "HOUSTON_RUNNERS=3",
                     "--entrypoint", "sh", RUNNER, "-c", Rails.root.join("lib/update-helper.sh").read ], helper_runs(fake).sole.args
    end
    assert_equal [ "v0.4.3", "v0.4.2", "running" ], [ update.to_version, update.from_version, update.status ]
    assert_enqueued_with(job: ServerUpdateJob, args: [ true ])
    assert_in_delta Time.current, update.started_at, 5

    ServerUpdate.delete_all
    use_fake_docker(docker) { |fake| ServerUpdate.start!("v0.5.0"); assert_includes helper_runs(fake).sole.args, "HOUSTON_UPDATE_TO=v0.5.0" }
  end

  test "refused before anything changes" do
    cases = {
      "not a release" => -> { ENV["HOUSTON_VERSION"] = "dev"; [ nil, docker ] },
      "a checkout's build" => -> { ENV.delete("HOUSTON_VERSION"); ENV["HOUSTON_SOURCE_SHA"] = "abc1234"; [ nil, docker ] },
      "not newer" => -> { [ "v0.4.2", docker ] },
      "the latest known is this one" => -> { Installation.current.update!(latest_release: "v0.4.2"); [ nil, docker ] },
      "the latest known is older" => -> { Installation.current.update!(latest_release: "v0.4.1"); [ nil, docker ] },
      "older" => -> { [ "v0.4.1", docker ] },
      "a bad tag" => -> { [ "v0.4.3; rm -rf /", docker ] },
      "a prerelease" => -> { [ "v0.5.0-rc.1", docker ] },
      "no latest known" => -> { Installation.current.update!(latest_release: nil); [ nil, docker ] },
      "not installed by the installer" => -> { [ nil, docker(labels: "{}") ] },
      "no runner image" => -> { ENV.delete("HOUSTON_RUNNER_IMAGE"); [ nil, docker ] },
      "the helper didn't start" => -> { [ nil, docker(run: false) ] }
    }
    words = { "not a release" => "runs dev, not a release", "a checkout's build" => "runs source abc1234, not a release",
              "not newer" => "v0.4.2 isn't newer",
              "the latest known is this one" => "already runs the latest release (v0.4.2)",
              "the latest known is older" => "already runs the latest release (v0.4.2)", "older" => "v0.4.1 isn't newer", "a bad tag" => "isn't a release (vX.Y.Z)",
              "a prerelease" => "isn't a release (vX.Y.Z)", "no latest known" => "no newer release is known",
              "not installed by the installer" => "wasn't started by Houston's installer", "no runner image" => "run the installer once more",
              "the helper didn't start" => "couldn't start the update: docker: Error response" }
    cases.each do |name, arrange|
      version, fake = arrange.call
      error = assert_raises(ServerUpdate::Refused, name) { use_fake_docker(fake) { ServerUpdate.start!(version) } }
      assert_match words.fetch(name), error.message, name
      assert_equal 0, ServerUpdate.count, "#{name}: a row was left"
      assert_equal (name == "the helper didn't start" ? 1 : 0), helper_runs(fake).size, name
      defaults!
    end
  end

  test "one update at a time" do
    use_fake_docker(docker) { ServerUpdate.start! }
    use_fake_docker(docker) do |fake|
      error = assert_raises(ServerUpdate::Refused) { ServerUpdate.start!("v0.5.0") }
      assert_match "v0.4.3 is already running", error.message
      assert_empty helper_runs(fake)
    end
    assert_equal 1, ServerUpdate.count
    assert_raises(ActiveRecord::RecordNotUnique, "the database holds the lock too") do
      ServerUpdate.insert!({ to_version: "v0.5.0", from_version: "v0.4.2", status: "running", started_at: Time.current })
    end
  end

  test "refused while a deploy or backup is busy, even one claimed meanwhile" do
    # (Busy is checked after the row is saved, so a refusal leaves no row.)
    project = make_project("equip")
    make_deploy(project, 1, "go")

    { "queued" => "deploy #2 of equip is queued", "in_flight" => "deploy #2 of equip is in flight" }.each do |status, words|
      deploy = make_deploy(project, 2, status)
      use_fake_docker(docker) do |fake|
        error = assert_raises(ServerUpdate::Refused) { ServerUpdate.start! }
        assert_match words, error.message
        assert_empty helper_runs(fake)
      end
      deploy.destroy!
    end

    %w[queued running].each do |status|
      run = project.backup_runs.create!(location: storage_locations(:unas), kind: "auto", reason: "manual", status:, heartbeat_at: Time.current)
      error = assert_raises(ServerUpdate::Refused) { use_fake_docker(docker) { ServerUpdate.start! } }
      assert_match "a backup of equip is #{status}", error.message
      run.destroy!
    end
    assert_equal 0, ServerUpdate.count

    # A deploy queued and claimed after the first check, before the row was
    # saved: the re-check after saving catches it, and no helper starts.
    fake, claimed = docker, false
    claim = lambda do |*, payload|
      next if claimed || !payload[:sql].start_with?('INSERT INTO "server_updates"')
      claimed = true
      make_deploy(project, 3, "in_flight")
    end
    ActiveSupport::Notifications.subscribed(claim, "sql.active_record") do
      error = assert_raises(ServerUpdate::Refused) { use_fake_docker(fake) { ServerUpdate.start! } }
      assert_match "deploy #3 of equip is in flight", error.message
    end
    assert claimed, "the claim landed between the check and the save"
    assert_empty helper_runs(fake)
    assert_equal 0, ServerUpdate.count
  end

  test "deploys and backups wait for the update" do
    project = make_project("equip")
    make_deploy(project, 1, "go")
    use_fake_docker(docker) { ServerUpdate.start! }

    queued = make_deploy(project, 2, "queued")
    run = project.backup_runs.create!(location: storage_locations(:unas), kind: "auto", reason: "manual", status: "queued", heartbeat_at: Time.current)
    assert_nil Deploy.claim_next!(runner: "houston-runner-1")
    assert_equal :busy, BackupRun.claim!(run)
    assert_equal %w[queued queued], [ queued.reload.status, run.reload.status ]

    ServerUpdate.sole.update!(status: "go", finished_at: Time.current)
    assert_equal queued, Deploy.claim_next!(runner: "houston-runner-1")&.first
    queued.update!(status: "go", finished_at: Time.current)
    assert_kind_of String, BackupRun.claim!(run)
  end
end
