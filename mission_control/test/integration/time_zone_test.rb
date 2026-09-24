require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/backup_helpers"
require_relative "../support/api_helpers"

# Mission Control shows every time in the zone chosen in Settings, not UTC
# ("I changed to America/Chicago and everything is still showing UTC").
class TimeZoneTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers
  include ApiHelpers

  setup do
    Installation.current.update!(time_zone: "America/Chicago")
    @project = make_backup_project
    sign_in_as users(:one)
  end

  test "pages show times in the Settings zone" do
    started = Time.utc(2026, 9, 24, 12, 0, 0) # 07:00 in Chicago (CDT)
    @project.deploys.create!(number: 42, sha: "c" * 40, ref: "refs/heads/main", status: "go", token_digest: "d",
                             heartbeat_at: started, created_at: started, finished_at: started + 90)

    get project_path("equip")
    assert_select "[data-deploy=42]", /24 Sep 07:00/
    assert_select ".status__clock", /\A\d\d:\d\d C[DS]T\z/

    get project_deploy_path("equip", 42)
    assert_select ".deploy-head__clock", /STARTED 07:00:00 CDT/

    listing = FakeDocker.new { DockerCommand::Result.new(success: true, output: [ snapshot_json(id: "11111111", time: "2026-09-21T03:00:00Z") ].to_json) }
    use_fake_docker(listing) { get project_snapshots_path("equip") }
    assert_select "[data-snapshot=11111111]", /20 Sep 22:00/ # 03:00 UTC is 22:00 the day before
  end

  test "a deploy page's live updates show the Settings zone too" do
    deploy, token, = Deploy.start!(@project, sha: "d" * 40, ref: "refs/heads/main")
    streams = capture_turbo_stream_broadcasts(deploy) do
      patch "/api/deploys/#{deploy.id}", params: { step: "Build" }.to_json, headers: api_headers.merge("X-Houston-Deploy-Token" => token)
    end
    status = streams.find { |s| s["target"] == "deploy_status" }
    assert_match(/STARTED \d\d:\d\d:\d\d C[DS]T/, status.at("template").inner_html)
  end
end
