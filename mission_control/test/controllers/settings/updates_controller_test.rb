require "test_helper"
require_relative "../../support/fake_docker"

# Settings › Releases: the version, checking for a newer one, updating, and
# each update's log (docs/plans/update-from-mission-control.md, Batch 3).
class Settings::UpdatesControllerTest < ActionDispatch::IntegrationTest
  include FakeDockerHelper

  LABELS = %({"com.docker.compose.project.config_files":"/opt/houston/compose.yml","com.docker.compose.project.working_dir":"/opt/houston"}).freeze
  RELEASES = "https://api.github.com/repos/scttymn/houston/releases/latest".freeze
  LOG = "==> houston update: installing v0.4.3\n==> Docker is installed\n==> Pulling Houston v0.4.3\n".freeze

  setup do
    ENV["HOUSTON_VERSION"] = "v0.4.2"
    ENV["HOUSTON_RUNNER_IMAGE"] = "houston/runner:local"
    Installation.current.update!(latest_release: "v0.4.3", latest_release_url: "https://github.com/scttymn/houston/releases/tag/v0.4.3", latest_release_checked_at: 2.hours.ago)
  end

  teardown { %w[HOUSTON_VERSION HOUSTON_RUNNER_IMAGE].each { ENV.delete(it) } }

  def docker = FakeDocker.new { |args| DockerCommand::Result.new(success: true, output: LABELS) if args.first == "inspect" }
  def runs(fake) = fake.calls.count { |c| c.args.first == "run" }
  def github(tag) = stub_request(:get, RELEASES).to_return(body: { tag_name: tag, html_url: "https://github.com/scttymn/houston/releases/tag/#{tag}" }.to_json)
  def update!(**attrs) = ServerUpdate.create!({ to_version: "v0.4.3", from_version: "v0.4.2", status: "go", started_at: 1.hour.ago, finished_at: 1.hour.ago + 40, log: LOG }.merge(attrs))

  test "Settings › Releases: the version, checking, updating, and the updates" do
    sign_in_as users(:one)
    get settings_path
    assert_select ".section-nav a[href='#releases']", "Releases"
    assert_select "section#releases" do
      assert_select ".panel__title", "Releases"
      assert_select ".panel__meta .eyebrow", "V0.4.2"
      assert_select ".panel__meta form[action='#{check_settings_updates_path}'] button", "Check for updates"
      assert_select ".panel__meta form[action='#{settings_updates_path}']" do
        assert_select "input[name=version][value='v0.4.3']", 1
        assert_select "button[data-turbo-confirm*='Mission Control restarts']", "Update to v0.4.3"
      end
      assert_select ".panel__row", /v0\.4\.3 is out \(checked about 2 hours ago\)/
      assert_select ".panel__row a[href='https://github.com/scttymn/houston/releases/tag/v0.4.3']", /Release notes/
      assert_select ".panel__row", /No updates from Mission Control yet/
    end

    ENV["HOUSTON_VERSION"] = "v0.4.3"
    earlier = update!(to_version: "v0.4.2", from_version: "v0.4.1", started_at: 2.days.ago, finished_at: 2.days.ago + 40)
    failed = update!(status: "rolled_back", started_at: 3.hours.ago, finished_at: 3.hours.ago + 95)
    good = update!
    get settings_path
    assert_select "section#releases" do
      assert_select ".panel__meta form[action='#{settings_updates_path}']", 0, "nothing newer"
      assert_select ".panel__row", /v0\.4\.3 is the latest release/
      assert_select ".history__row", 3
      assert_select ".history__row:first-of-type" do
        assert_select ".mono", "##{good.id}"
        assert_select ".mono", "v0.4.3"
        assert_select ".history__what", /from v0\.4\.2/
        assert_select ".state", "GO"
        assert_select "a[href='#{settings_update_path(good)}']", "Log"
      end
      assert_select ".history__row--no-go .history__error", /failed; went back to v0\.4\.2/
      assert_select ".history__row--no-go a[href='#{settings_update_path(failed)}']", "Log"
      assert_select "a[href='#{settings_update_path(earlier)}']", "Log"
    end
  end

  test "while one runs, Settings › Releases says so and keeps checking" do
    sign_in_as users(:one)
    running = update!(status: "running", finished_at: nil, started_at: 1.minute.ago, step: "Pulling Houston v0.4.3")
    get settings_path
    assert_select "section#releases" do
      assert_select "form[action='#{settings_updates_path}']", 0, "no second update meanwhile"
      assert_select ".panel__row[data-controller=refresh]", /Updating to v0\.4\.3 · Pulling Houston v0\.4\.3/
      assert_select ".history__row .state", "UPDATING"
      assert_select "a[href='#{settings_update_path(running)}']", "Log"
    end
  end

  test "an update's log" do
    sign_in_as users(:one)
    update = update!(status: "running", finished_at: nil, started_at: 1.minute.ago, step: "Pulling Houston v0.4.3")
    get settings_update_path(update)
    assert_response :success
    assert_select "nav.crumbs a[href='#{settings_path(anchor: "releases")}']", "Releases"
    assert_select "h1", "Update ##{update.id}"
    assert_select ".deploy-head__meta", /v0\.4\.2 → v0\.4\.3/
    assert_select "[data-controller=refresh]", 1, "it refreshes while the update runs"
    assert_select ".serving", /v0\.4\.2 keeps running your apps/
    assert_select ".deploy-steps li[data-state=done]", 2
    assert_select ".deploy-steps li[data-state=current]", /Pulling Houston v0\.4\.3/
    assert_select ".log-panel__live", "LIVE"
    assert_select "pre.log[data-follow-target=log]", /Docker is installed/

    update.update!(status: "rolled_back", finished_at: Time.current, log: LOG + "curl: (22) 404\n==> houston update: v0.4.3 didn't install; putting v0.4.2 back\n==> Starting Houston\n")
    get settings_update_path(update)
    assert_select "[data-controller=refresh]", 0
    assert_select ".log-panel__live", 0
    assert_select ".notice--nogo .notice__text", /The update to v0\.4\.3 failed, so this server went back to v0\.4\.2\./
    assert_select ".deploy-steps li[data-state=failed]", 1
    assert_select ".deploy-steps li[data-state=failed]", /Pulling Houston v0\.4\.3/, "the step that broke"
    assert_select ".deploy-steps li[data-state=done]", 4, "the rest, putting v0.4.2 back included"

    update.update!(status: "no_go", log: LOG + "==> houston update: v0.4.3 didn't install; putting v0.4.2 back\n==> Pulling Houston v0.4.2\n" +
                                          "==> houston update: v0.4.2 didn't install either; run the installer on the server\n")
    get settings_update_path(update)
    assert_select ".deploy-steps li[data-state=failed]", 2, "the new version's pull, and the old one's"
  end

  test "Check for updates asks GitHub now, and says what it found" do
    sign_in_as users(:one)
    github("v0.4.2")
    post check_settings_updates_path
    assert_redirected_to settings_path(anchor: "releases")
    follow_redirect!
    assert_select ".toast.toast--go[role=status][data-controller=toast]", "v0.4.2 is the latest."

    github("v0.4.5")
    post check_settings_updates_path
    follow_redirect!
    assert_select ".toast--go", "v0.4.5 is out."
    assert_equal "v0.4.5", Installation.current.reload.latest_release

    stub_request(:get, RELEASES).to_timeout
    post check_settings_updates_path
    follow_redirect!
    assert_select ".toast.toast--nogo", "Couldn't reach GitHub just now; try again in a minute."
    assert_equal "v0.4.5", Installation.current.reload.latest_release, "what we knew stays"
  end

  test "Update starts it and opens its log; refused, it says why" do
    sign_in_as users(:one)
    use_fake_docker(docker) { |fake| post settings_updates_path, params: { version: "v0.4.3" }; @runs = runs(fake) }
    assert_redirected_to settings_update_path(ServerUpdate.sole)
    follow_redirect!
    assert_select ".toast--go", /Updating to v0\.4\.3/
    assert_equal 1, @runs

    ServerUpdate.delete_all
    use_fake_docker(docker) { |fake| post settings_updates_path, params: { version: "v0.4.2" }; @runs = runs(fake) }
    assert_redirected_to settings_path(anchor: "releases")
    follow_redirect!
    assert_select ".toast--nogo", /isn't newer/
    assert_equal [ 0, 0 ], [ @runs, ServerUpdate.count ]
  end

  test "signed out, nothing is shown, checked or started" do
    update = update!
    get settings_update_path(update)
    assert_redirected_to sign_in_path
    github("v0.4.5")
    post check_settings_updates_path
    assert_redirected_to sign_in_path
    assert_not_requested :get, RELEASES
    use_fake_docker(docker) { |fake| post settings_updates_path, params: { version: "v0.4.3" }; @runs = runs(fake) }
    assert_redirected_to sign_in_path
    assert_equal [ 0, 1 ], [ @runs, ServerUpdate.count ]
  end
end
