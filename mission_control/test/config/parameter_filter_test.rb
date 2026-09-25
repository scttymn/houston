require "test_helper"

# What never reaches Rails' log (security fixes, L3): a deploy's log (it can
# echo what the app printed), its error, and the one-time setup code.
class ParameterFilterTest < ActiveSupport::TestCase
  test "deploy logs, errors and the setup code are filtered" do
    filter = ActiveSupport::ParameterFilter.new(Rails.application.config.filter_parameters)
    filtered = filter.filter("log" => "SECRET=1", "error" => "boom", "setup" => { "code" => "ABCD-EFGH" }, "sha" => "abc")
    assert_equal({ "log" => "[FILTERED]", "error" => "[FILTERED]", "setup" => { "code" => "[FILTERED]" }, "sha" => "abc" }, filtered)
  end
end
