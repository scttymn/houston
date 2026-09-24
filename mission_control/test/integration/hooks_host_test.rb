require "test_helper"

# The tunnel sends hooks.<base>/<one-segment path> to Mission Control; only
# the webhook and the reachability ping may answer there.
class HooksHostTest < ActionDispatch::IntegrationTest
  test "hooks.<base> serves only the webhook and the ping" do
    host! "hooks.svnmns.com"
    [ [ :get, "/sign-in" ], [ :get, "/" ], [ :post, "/link" ], [ :get, "/projects/x" ], [ :get, "/up" ], [ :get, "/setup" ],
      [ :post, "/api/projects/sync" ], [ :get, "/equip" ], [ :post, "/projects/equip/secrets/X/generate" ] ].each do |verb, path|
      send(verb, path)
      assert_response :not_found, "#{verb.upcase} #{path}"
      assert_empty response.body, "#{verb.upcase} #{path}"
    end

    get "/ping"
    assert_response :success
    assert_equal Installation.identity, response.body

    host! "admin.svnmns.com"
    get "/sign-in"
    assert_response :success
  end
end
