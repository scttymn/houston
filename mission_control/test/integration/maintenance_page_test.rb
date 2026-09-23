require "test_helper"
require_relative "../support/project_helpers"

# Mission Control receives an app's hostnames only while the tunnel routes
# them here (maintenance). For those hosts it serves the maintenance page and
# nothing else, and a stale route never reaches sign-in or the API.
class MaintenancePageTest < ActionDispatch::IntegrationTest
  include ProjectHelpers

  setup do
    @project = make_project("equip", domains: %w[equipping.com])
    @project.update!(maintenance_since: Time.current, maintenance_by: "admin@example.com", maintenance_message: "Back by <b>10:00</b>")
  end

  test "the maintenance page" do
    [ "equip.svnmns.com", "equipping.com" ].each do |host|
      host! host
      [ "/", "/deep/path?x=1", "/session/new", "/api/v1/me" ].each do |path|
        get path
        assert_response :service_unavailable, "#{host}#{path}"
        assert_equal "60", response.headers["Retry-After"]
        assert_includes response.headers["Cache-Control"], "no-store"
        assert_select "h1", /equip/
        assert_includes response.body, "is down for maintenance"
        assert_includes response.body, "Back by &lt;b&gt;10:00&lt;/b&gt;", "the message is escaped"
        assert_not_includes response.body, "Mission Control"
        assert_select "link[rel=stylesheet], script[src]", 0
      end
      post "/session", params: { email_address: "x", password: "y" }
      assert_response :service_unavailable
    end
  end

  # Production checks CSRF tokens (tests don't by default): a POST to an app
  # host still gets the page, not Rails' 422.
  test "the page answers a POST with forgery protection on" do
    ActionController::Base.allow_forgery_protection = true
    host! "equip.svnmns.com"
    post "/checkout", params: { item: "1" }
    assert_response :service_unavailable
    assert_includes response.body, "is down for maintenance"
    assert_nil cookies["_mission_control_session"], "no session on an app host"
  ensure
    ActionController::Base.allow_forgery_protection = false
  end

  test "a project's own page" do
    @project.update!(maintenance_page: "<!doctype html><title>{{project}}</title><h1>{{project}} is resting</h1><p class=m>{{message}}</p><p>{{other}}</p>",
                     maintenance_message: "<script>alert(1)</script> & back soon")
    host! "equip.svnmns.com"
    get "/anything"
    assert_response :service_unavailable
    assert_equal "60", response.headers["Retry-After"]
    assert_includes response.headers["Cache-Control"], "no-store"
    assert_select "h1", "equip is resting"
    assert_includes response.body, "&lt;script&gt;alert(1)&lt;/script&gt; &amp; back soon"
    assert_not_includes response.body, "<script>alert(1)"
    assert_includes response.body, "{{other}}", "nothing else is interpreted"

    @project.update!(maintenance_message: nil)
    get "/"
    assert_select "p.m", ""
  end

  test "an app host not in maintenance is a plain 404" do
    @project.update!(maintenance_since: nil)
    host! "equip.svnmns.com"
    get "/session/new"
    assert_response :not_found
    assert_not_includes response.body, "Sign in"
    post "/api/v1/projects/equip/maintenance"
    assert_response :not_found
  end

  test "Mission Control's own hosts are untouched" do
    host! "admin.svnmns.com"
    get "/session/new"
    assert_response :success
  end
end
