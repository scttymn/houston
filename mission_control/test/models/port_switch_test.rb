require "test_helper"
require_relative "../support/fake_docker"

# Port 3000, open or closed from Settings (security fixes, H3): the choice
# is saved (the installer reads it on every run) and applied by having
# Mission Control recreated from its compose.yml with HOUSTON_BIND set.
class PortSwitchTest < ActiveSupport::TestCase
  include FakeDockerHelper

  RUNNER = "ghcr.io/scttymn/houston-runner:v0.4.0@sha256:#{"a" * 64}".freeze
  LABELS = %({"com.docker.compose.project":"houston","com.docker.compose.project.config_files":"/opt/houston/compose.yml","com.docker.compose.project.working_dir":"/opt/houston"}).freeze

  setup { ENV["HOUSTON_RUNNER_IMAGE"] = RUNNER }
  teardown { ENV.delete("HOUSTON_RUNNER_IMAGE") }

  def docker(labels: LABELS, run: true)
    FakeDocker.new do |args|
      if args.first == "inspect" then DockerCommand::Result.new(success: true, output: labels)
      elsif args.first == "run" && !run then failure("Conflict. The container name \"/houston-close-port\" is already in use")
      end
    end
  end

  def helper_run(bind) = [ "run", "-d", "--rm", "--name", "houston-port-3000", "--user", "0", "-e", "HOUSTON_BIND=#{bind}",
                           "-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", "/opt/houston:/opt/houston:ro",
                           "--entrypoint", "sh", RUNNER, "-c",
                           "sleep 5 && exec docker compose -f /opt/houston/compose.yml up -d --no-deps mission-control" ]

  test "closing saves the choice and recreates Mission Control on 127.0.0.1" do
    use_fake_docker(docker) do |fake|
      PortSwitch.set!(open: false)
      assert_equal helper_run("127.0.0.1"), fake.calls.find { |c| c.args.first == "run" }.args
    end
    assert_not Installation.current.reload.port_open
  end

  test "opening saves the choice and recreates it on every interface" do
    Installation.current.update!(port_open: false)
    use_fake_docker(docker) do |fake|
      PortSwitch.set!(open: true)
      assert_equal helper_run("0.0.0.0"), fake.calls.find { |c| c.args.first == "run" }.args
    end
    assert Installation.current.reload.port_open
  end

  test "refused, with nothing saved, when it can't be applied" do
    { "not installed by the installer (no compose labels)" => -> { docker(labels: "{}") },
      "no runner image" => -> { ENV.delete("HOUSTON_RUNNER_IMAGE"); docker },
      "the helper didn't start" => -> { docker(run: false) } }.each do |name, fake|
      error = assert_raises(PortSwitch::Refused, name) { use_fake_docker(fake.call) { PortSwitch.set!(open: false) } }
      assert_predicate error.message, :present?, name
      assert Installation.current.reload.port_open, "#{name}: the choice isn't saved"
      ENV["HOUSTON_RUNNER_IMAGE"] = RUNNER
    end
  end

  test "a new install's port is open (setup needs it)" do
    assert Installation.new.port_open
  end
end
