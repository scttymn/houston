require "test_helper"
require "rake"

class SetupCodeTaskTest < ActiveSupport::TestCase
  setup do
    Rails.application.load_tasks unless Rake::Task.task_defined?("houston:setup_code")
    Rake::Task["houston:setup_code"].reenable
  end

  test "prints a new code before setup, storing only its digest" do
    Session.delete_all
    User.delete_all
    old = SetupCode.issue!

    out, _ = capture_io { Rake::Task["houston:setup_code"].invoke }

    code = out.strip
    assert_match(/\A[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}\z/, code)
    assert_equal 1, SetupCode.count
    refute_includes SetupCode.first.attributes.values.map(&:to_s).join(" "), code.delete("-")
    assert SetupCode.matching(code), "the new code works"
    assert_nil SetupCode.matching(old), "the old code stops working"
  end

  test "refuses once an admin exists" do
    assert User.exists?, "fixtures provide an admin"

    _, err = capture_io do
      error = assert_raises(SystemExit) { Rake::Task["houston:setup_code"].invoke }
      assert_equal 1, error.status
    end

    assert_match(/setup is complete/, err)
    assert_equal 0, SetupCode.count
  end
end
