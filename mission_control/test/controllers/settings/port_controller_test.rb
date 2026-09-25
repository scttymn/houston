require "test_helper"
require_relative "../../support/fake_docker"

# Settings › Port 3000 (security fixes, H3): what port 3000 is bound to
# now, and a button to close or open it.
class Settings::PortControllerTest < ActionDispatch::IntegrationTest
  include FakeDockerHelper

  LABELS = %({"com.docker.compose.project.config_files":"/opt/houston/compose.yml","com.docker.compose.project.working_dir":"/opt/houston"}).freeze

  setup do
    ENV["HOUSTON_RUNNER_IMAGE"] = "houston/runner:local"
    Rails.cache.clear
  end

  teardown do
    ENV.delete("HOUSTON_RUNNER_IMAGE")
    Rails.cache.clear
  end

  def docker(host_ip)
    FakeDocker.new do |args|
      next unless args.first == "inspect"
      output = args.include?("{{json .HostConfig.PortBindings}}") ? %({"80/tcp":[{"HostIp":"#{host_ip}","HostPort":"3000"}]}) : LABELS
      DockerCommand::Result.new(success: true, output:)
    end
  end

  test "it says what port 3000 is bound to, and offers the other" do
    sign_in_as users(:one)
    use_fake_docker(docker("0.0.0.0")) { get settings_path }
    assert_select "section#port" do
      assert_select ".panel__row", /OPEN\s+to your network, over plain HTTP/
      assert_select "form[action='#{settings_port_path}'] input[name=open][value='0']", 1
      assert_select "button, input[type=submit]", /Close port 3000/
      assert_select ".panel__row", /public address.*firewall/
    end

    Rails.cache.clear
    use_fake_docker(docker("127.0.0.1")) { get settings_path }
    assert_select "section#port .panel__row", /CLOSED\s+only on this server \(127\.0\.0\.1\)/
    assert_select "section#port input[name=open][value='1']", 1

    Rails.cache.clear
    use_fake_docker(docker("100.64.1.2")) { get settings_path }
    assert_select "section#port .panel__row", /OPEN\s+on 100\.64\.1\.2/

    Rails.cache.clear
    use_fake_docker(FakeDocker.new { failure("permission denied") }) { get settings_path }
    assert_select "section#port .panel__row", /can't tell/i
    assert_select "section#port form", 0
  end

  test "closing it from port 3000 goes on to admin.<base> first" do
    sign_in_as users(:one)
    use_fake_docker(docker("0.0.0.0")) { |fake| patch settings_port_path, params: { open: "0" }; @runs = fake.calls.count { |c| c.args.first == "run" } }
    assert_redirected_to "https://admin.svnmns.com/settings#port"
    assert_equal 1, @runs
    assert_not Installation.current.reload.port_open
  end

  test "through the tunnel, it stays on Settings and says what's happening" do
    post session_path, params: { email_address: users(:one).email_address, password: "password" }, headers: { "Cf-Ray" => "8f-MCI" }
    use_fake_docker(docker("127.0.0.1")) { patch settings_port_path, params: { open: "1" }, headers: { "Cf-Ray" => "8f-MCI" } }
    assert_redirected_to settings_path(anchor: "port")
    assert_match(/restarts for a few seconds/, flash[:notice])
    assert Installation.current.reload.port_open
  end

  test "refused: nothing changes, and it says why" do
    sign_in_as users(:one)
    ENV.delete("HOUSTON_RUNNER_IMAGE")
    use_fake_docker(docker("0.0.0.0")) { |fake| patch settings_port_path, params: { open: "0" }; @runs = fake.calls.count { |c| c.args.first == "run" } }
    assert_redirected_to settings_path(anchor: "port")
    assert_match(/can't/i, flash[:alert])
    assert_equal 0, @runs
    assert Installation.current.reload.port_open
  end

  test "signed out, nothing changes" do
    use_fake_docker(docker("0.0.0.0")) { |fake| patch settings_port_path, params: { open: "0" }; @runs = fake.calls.count { |c| c.args.first == "run" } }
    assert_redirected_to sign_in_path
    assert_equal 0, @runs
    assert Installation.current.reload.port_open
  end
end
