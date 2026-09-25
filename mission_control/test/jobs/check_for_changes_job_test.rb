require "test_helper"

# One check per project at a time (security fixes, review): a webhook's
# check and the poll's could otherwise interleave, and the one holding
# older refs would switch the queued deploy back to an older commit.
class CheckForChangesJobTest < ActiveJob::TestCase
  test "one check per project at a time" do
    assert_equal 1, CheckForChangesJob.concurrency_limit
    assert_equal CheckForChangesJob.new(7).concurrency_key, CheckForChangesJob.new(7).concurrency_key
    assert_not_equal CheckForChangesJob.new(7).concurrency_key, CheckForChangesJob.new(8).concurrency_key
  end
end
