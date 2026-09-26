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
    assert_redirected_to server_update_path
    assert_match "Updating to v0.4.3", flash[:notice]
    assert_equal [ 1, "v0.4.3" ], [ @runs, ServerUpdate.sole.to_version ]
  end

  test "refused: nothing starts, and it says why" do
    sign_in_as users(:one)
    use_fake_docker(docker) { |fake| post server_update_path, params: { version: "v0.4.2" }; @runs = runs(fake) }
    assert_redirected_to server_update_path
    assert_match(/isn't newer/, flash[:alert])
    assert_equal [ 0, 0 ], [ @runs, ServerUpdate.count ]
  end

  RELEASES = "https://api.github.com/repos/scttymn/houston/releases/latest".freeze

  def github(tag) = stub_request(:get, RELEASES).to_return(body: { tag_name: tag, html_url: "https://github.com/scttymn/houston/releases/tag/#{tag}" }.to_json)

  test "check asks GitHub now, and says what it found" do
    sign_in_as users(:one)
    github("v0.4.2")
    post check_server_update_path
    assert_redirected_to server_update_path
    assert_equal "v0.4.2 is the latest.", flash[:notice]
    follow_redirect!
    assert_select ".toast.toast--go[role=status][data-controller=toast]", "v0.4.2 is the latest."

    github("v0.4.5")
    post check_server_update_path
    assert_equal "v0.4.5 is out.", flash[:notice]
    assert_equal "v0.4.5", Installation.current.reload.latest_release

    stub_request(:get, RELEASES).to_timeout
    post check_server_update_path
    assert_equal "Couldn't reach GitHub just now; try again in a minute.", flash[:alert]
    assert_equal "v0.4.5", Installation.current.reload.latest_release, "what we knew stays"
  end

  test "Houston's page: the version, checking, and updating" do
    sign_in_as users(:one)
    Installation.current.update!(latest_release: "v0.4.2", latest_release_checked_at: 2.hours.ago)
    get server_update_path
    assert_response :success
    assert_select "nav.crumbs a[href='#{root_path}']", "Projects"
    assert_select "h1", "Houston v0.4.2"
    assert_select ".houston-head__meta", /v0\.4\.2 is the latest release \(checked about 2 hours ago\)/
    assert_select "form[action='#{check_server_update_path}'] button", "Check now"
    assert_select "form[action='#{server_update_path}']", 0, "nothing newer to update to"
    assert_select ".log-panel .log", /No update has run from Mission Control yet/

    Installation.current.update!(latest_release: "v0.4.3", latest_release_url: "https://github.com/scttymn/houston/releases/tag/v0.4.3")
    get server_update_path
    assert_select ".houston-head__meta", /v0\.4\.3 is out/
    assert_select ".houston-head__meta a[href='https://github.com/scttymn/houston/releases/tag/v0.4.3']", /Release notes/
    assert_select "form[action='#{server_update_path}']" do
      assert_select "input[name=version][value='v0.4.3']", 1
      assert_select "button[data-turbo-confirm*='Mission Control restarts']", "Update to v0.4.3"
    end
  end

  test "Houston's page follows an update's log" do
    sign_in_as users(:one)
    log = "==> houston update: installing v0.4.3\n==> Docker is installed\n==> Pulling Houston v0.4.3\n"
    update = ServerUpdate.create!(to_version: "v0.4.3", from_version: "v0.4.2", status: "running", started_at: 1.minute.ago, step: "Pulling Houston v0.4.3", log:)
    get server_update_path
    assert_select "[data-controller=refresh]", 1, "it refreshes while the update runs"
    assert_select "form[action='#{server_update_path}']", 0, "no second update meanwhile"
    assert_select ".houston-serving", /v0\.4\.2 keeps running your apps/
    assert_select ".deploy-steps li", 3
    assert_select ".deploy-steps li[data-state=done]", 2
    assert_select ".deploy-steps li[data-state=current]", /Pulling Houston v0\.4\.3/
    assert_select ".log-panel__live", "LIVE"
    assert_select ".log-panel__what", "v0.4.2 → v0.4.3"
    assert_select "pre.log[data-follow-target=log]", /Docker is installed/

    update.update!(status: "rolled_back", finished_at: Time.current, log: log + "curl: (22) 404\n==> houston update: v0.4.3 didn't install; putting v0.4.2 back\n==> Starting Houston\n")
    get server_update_path
    assert_select "[data-controller=refresh]", 0
    assert_select ".log-panel__live", 0
    assert_select ".notice--nogo .notice__text", /The update to v0\.4\.3 failed, so this server went back to v0\.4\.2\./
    assert_select ".deploy-steps li[data-state=failed]", 1
    assert_select ".deploy-steps li[data-state=failed]", /Pulling Houston v0\.4\.3/, "the step that broke"
    assert_select ".deploy-steps li[data-state=done]", 4, "the rest, putting v0.4.2 back included"

    update.update!(status: "no_go", log: log + "==> houston update: v0.4.3 didn't install; putting v0.4.2 back\n==> Pulling Houston v0.4.2\n" +
                                            "==> houston update: v0.4.2 didn't install either; run the installer on the server\n")
    get server_update_path
    assert_select ".deploy-steps li[data-state=failed]", 2, "the new version's pull, and the old one's"

    # Another update, recorded later: the page shows the newest; ?id= shows any.
    later = ServerUpdate.create!(to_version: "v0.4.4", from_version: "v0.4.2", status: "go", started_at: 1.minute.ago, finished_at: Time.current, log: "==> Starting Houston\n")
    get server_update_path
    assert_select ".log-panel__what", "v0.4.2 → v0.4.4"
    assert_select ".houston-updates a[href=?]", server_update_path(id: update.id), /v0\.4\.2 → v0\.4\.3/
    assert_select ".houston-updates li.is-current a[href=?]", server_update_path(id: later.id)
    get server_update_path(id: update.id)
    assert_select ".log-panel__what", "v0.4.2 → v0.4.3"
    assert_select ".houston-updates li.is-current a[href=?]", server_update_path(id: update.id)
  end

  test "signed out, nothing is shown" do
    get server_update_path
    assert_redirected_to sign_in_path
  end

  test "signed out, nothing is checked" do
    github("v0.4.5")
    post check_server_update_path
    assert_redirected_to sign_in_path
    assert_not_requested :get, RELEASES
  end

  test "signed out, nothing starts" do
    use_fake_docker(docker) { |fake| post server_update_path, params: { version: "v0.4.3" }; @runs = runs(fake) }
    assert_redirected_to sign_in_path
    assert_equal [ 0, 0 ], [ @runs, ServerUpdate.count ]
  end
end
