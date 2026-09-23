require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/tunnel_helpers"

class ProjectMaintenanceTest < ActionDispatch::IntegrationTest
  include ProjectHelpers
  include TunnelHelpers

  setup do
    connect_tunnel
    # The header's tunnel status.
    cf(:get, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}", result: { id: TUNNEL, name: "houston-svnmns", connections: [ { id: "c0" } ] })
    @project = make_project("equip")
  end

  test "turning the maintenance page on and off from the project page" do
    sign_in_as users(:one)
    pushes = record_pushes
    get project_path("equip")
    assert_select "form[action='#{project_maintenance_path("equip")}'] [data-turbo-confirm]"

    patch project_maintenance_path("equip"), params: { on: "1", message: "Back by 10:00" }
    assert_redirected_to project_path("equip")
    follow_redirect!
    assert_select ".notice--hold", /MAINTENANCE.*equip shows a maintenance page.*#{users(:one).email_address}.*Back by 10:00/m
    assert_equal 1, pushes.size

    patch project_maintenance_path("equip"), params: { on: "0" }
    follow_redirect!
    assert_select ".notice--hold", 0
    assert_nil @project.reload.maintenance_since

    patch project_maintenance_path("equip"), params: { on: "1", message: "x" * 501 }
    follow_redirect!
    assert_select ".notice--nogo", /maximum is 500 characters/
    assert_nil @project.reload.maintenance_since
  end

  test "Cloudflare refusing is shown" do
    sign_in_as users(:one)
    record_pushes(status: 400, message: "Tunnel configuration is invalid")
    patch project_maintenance_path("equip"), params: { on: "1" }
    follow_redirect!
    assert_select ".notice--nogo", /Tunnel configuration is invalid/
  end

  test "maintenance needs the admin" do
    pushes = record_pushes
    patch project_maintenance_path("equip"), params: { on: "1" }
    assert_redirected_to new_session_path
    assert_empty pushes
  end

  test "previewing the page" do
    sign_in_as users(:one)
    get project_path("equip")
    assert_select "iframe[sandbox=''][src='#{preview_project_maintenance_path("equip")}']"

    get preview_project_maintenance_path("equip")
    assert_response :success
    assert_equal "sandbox", response.headers["Content-Security-Policy"]
    assert_includes response.body, "is down for maintenance", "the default without a page"

    @project.update!(maintenance_page: "<h1>{{project}} is resting</h1><p>{{message}}</p><script>parent.document.title='x'</script>")
    get preview_project_maintenance_path("equip")
    assert_equal "sandbox", response.headers["Content-Security-Policy"]
    assert_includes response.body, "equip is resting"
    assert_includes response.body, "(your message)"

    delete session_path
    get preview_project_maintenance_path("equip")
    assert_redirected_to new_session_path
  end
end
