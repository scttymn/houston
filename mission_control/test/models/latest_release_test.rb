require "test_helper"

# The latest Houston release, checked on a schedule (docs/plans/update-available.md).
class LatestReleaseTest < ActiveSupport::TestCase
  URL = "https://api.github.com/repos/scttymn/houston/releases/latest"

  def github(tag: "v0.1.1", url: "https://github.com/scttymn/houston/releases/tag/v0.1.1", status: 200)
    stub_request(:get, URL).to_return(status:, body: { tag_name: tag, html_url: url }.to_json, headers: { "Content-Type" => "application/json" })
  end

  def refreshes(&) = capture_turbo_stream_broadcasts(FlightBoard::STREAM, &).count { |s| s["action"] == "refresh" }

  def capture_log
    io = StringIO.new
    original, Rails.logger = Rails.logger, ActiveSupport::Logger.new(io)
    yield
    io.string
  ensure
    Rails.logger = original
  end

  test "the check keeps the latest release" do
    github
    travel_to(Time.utc(2026, 9, 24, 22)) { assert_equal "v0.1.1", LatestRelease.check! }
    i = Installation.current
    assert_equal [ "v0.1.1", "https://github.com/scttymn/houston/releases/tag/v0.1.1", Time.utc(2026, 9, 24, 22) ],
                 [ i.latest_release, i.latest_release_url, i.latest_release_checked_at ]
  end

  test "a bad answer keeps what we knew" do
    github
    LatestRelease.check!
    [
      -> { github(tag: "v0.2; rm -rf /") },
      -> { github(url: "https://evil.example/houston") },
      -> { github(status: 403) },
      -> { stub_request(:get, URL).to_return(status: 200, body: "not json") },
      -> { stub_request(:get, URL).to_timeout }
    ].each do |answer|
      answer.call
      log = capture_log { assert_nil LatestRelease.check! }
      assert_match(/latest release/i, log, "a failed check is logged")
      assert_equal "v0.1.1", Installation.current.latest_release, "what we knew stays"
    end
  end

  test "a new release refreshes the board" do
    github
    assert_equal 1, refreshes { LatestRelease.check! }
    assert_equal 0, refreshes { LatestRelease.check! }, "the same release again"
    github(tag: "v0.2.0", url: "https://github.com/scttymn/houston/releases/tag/v0.2.0")
    assert_equal 1, refreshes { LatestRelease.check! }
  end

  test "the release check is scheduled" do
    recurring = YAML.load(ERB.new(Rails.root.join("config/recurring.yml").read).result, aliases: true)
    assert_equal({ "class" => "LatestReleaseJob", "schedule" => "every 6 hours" }, recurring.dig("production", "check_latest_release"))
    github
    LatestReleaseJob.perform_now
    assert_equal "v0.1.1", Installation.current.latest_release
  end
end
