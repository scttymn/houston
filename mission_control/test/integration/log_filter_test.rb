require "test_helper"

# Secret values arrive as form params (the project page's `value`) and API
# bodies; none of them may reach the log.
class LogFilterTest < ActiveSupport::TestCase
  test "secret-bearing params are filtered from the log" do
    filter = ActiveSupport::ParameterFilter.new(Rails.application.config.filter_parameters)
    params = { "value" => "sk_live_123", "deploy_key" => "-----BEGIN", "webhook_secret" => "whsec", "token" => "t", "repo_url" => "git@x:y.git" }

    filtered = filter.filter(params)

    %w[value deploy_key webhook_secret token].each { |key| assert_equal "[FILTERED]", filtered[key], key }
    assert_equal "git@x:y.git", filtered["repo_url"]
  end
end
