require "test_helper"

# Samples every app's CPU, memory and disk (docs/plans/app-stats.md).
class AppStatsJobTest < ActiveJob::TestCase
  test "it's scheduled every 30 seconds" do
    job = YAML.load_file(Rails.root.join("config/recurring.yml")).dig("production", "sample_app_stats")
    assert_equal [ "AppStatsJob", "every 30 seconds" ], job.values_at("class", "schedule")
  end
end
