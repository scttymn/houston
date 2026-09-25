require "test_helper"
require_relative "../support/fake_docker"

# The flight board's Update button (docs/plans/update-from-mission-control.md, row 10).
class ServerUpdatesControllerTest < ActionDispatch::IntegrationTest
  include FakeDockerHelper

  LABELS = %({"com.docker.compose.project.config_files":"/opt/houston/compose.yml","com.docker.compose.project.working_dir":"/opt/houston"}).freeze

  setup do
    ENV["HOUSTON_VERSION"] = "v0.4.2"
    ENV["HOUSTON_RUNNER_IMAGE"] = "houston/runner:local"
    Installation.current.update!(latest_release: "v0.4.3")
  end

  teardown { %w[HOUSTON_VERSION HOUSTON_RUNNER_IMAGE].each { ENV.delete(it) } }

  def docker = FakeDocker.new { |args| DockerCommand::Result.new(success: true, output: LABELS) if args.first == "inspect" }
  def runs(fake) = fake.calls.count { |c| c.args.first == "run" }

  test "it starts the update and says so" do
    sign_in_as users(:one)
    use_fake_docker(docker) { |fake| post server_update_path, params: { version: "v0.4.3" }; @runs = runs(fake) }
    assert_redirected_to root_path
    assert_match "Updating to v0.4.3", flash[:notice]
    assert_equal [ 1, "v0.4.3" ], [ @runs, ServerUpdate.sole.to_version ]
  end

  test "refused: nothing starts, and it says why" do
    sign_in_as users(:one)
    use_fake_docker(docker) { |fake| post server_update_path, params: { version: "v0.4.2" }; @runs = runs(fake) }
    assert_redirected_to root_path
    assert_match(/isn't newer/, flash[:alert])
    assert_equal [ 0, 0 ], [ @runs, ServerUpdate.count ]
  end

  test "signed out, nothing starts" do
    use_fake_docker(docker) { |fake| post server_update_path, params: { version: "v0.4.3" }; @runs = runs(fake) }
    assert_redirected_to sign_in_path
    assert_equal [ 0, 0 ], [ @runs, ServerUpdate.count ]
  end
end
