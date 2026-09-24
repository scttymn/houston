require "test_helper"

# When the flight board says an update is out (docs/plans/update-available.md).
class UpdateNoticeTest < ActiveSupport::TestCase
  test "only a release behind a newer one" do
    assert_equal "v0.1.1", UpdateNotice.newer("v0.1.0", "v0.1.1")
    assert_equal "v0.1.10", UpdateNotice.newer("v0.1.9", "v0.1.10"), "by number, not as text"
    assert_equal "v1.0.0", UpdateNotice.newer("v0.9.9", "v1.0.0")
    assert_nil UpdateNotice.newer("v0.1.1", "v0.1.1"), "current"
    assert_nil UpdateNotice.newer("v0.2.0", "v0.1.1"), "ahead of the latest"
    assert_nil UpdateNotice.newer("source 4c4f80a", "v0.1.1"), "a checkout build"
    assert_nil UpdateNotice.newer("dev", "v0.1.1")
    assert_nil UpdateNotice.newer("v0.1.0", nil), "never checked"
  end
end
