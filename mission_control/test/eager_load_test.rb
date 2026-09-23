require "test_helper"

# Production eager-loads app/ and lib/ at boot; tests don't. Loading
# everything here catches a file that can't be loaded (like a script that
# runs when required) before a server does.
class EagerLoadTest < ActiveSupport::TestCase
  test "everything production loads at boot loads" do
    assert_nothing_raised { Rails.application.eager_load! }
  end
end
