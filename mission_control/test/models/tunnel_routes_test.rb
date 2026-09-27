require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/tunnel_helpers"

class TunnelRoutesTest < ActiveSupport::TestCase
  include ProjectHelpers
  include TunnelHelpers

  test "the tunnel's rules, in order" do
    make_project("quiet")
    equip = make_project("equip", domains: %w[equipping.com www.equipping.com])
    equip.update!(maintenance_since: Time.current)

    mc = TunnelRoutes.mission_control
    assert_equal [
      { "hostname" => "admin.svnmns.com", "service" => mc },
      { "hostname" => "hooks.svnmns.com", "path" => "^/[a-z0-9-]+$", "service" => mc },
      { "hostname" => "hooks.svnmns.com", "service" => "http_status:404" },
      { "hostname" => "equip.svnmns.com", "service" => mc },
      { "hostname" => "equipping.com", "service" => mc },
      { "hostname" => "www.equipping.com", "service" => mc },
      { "service" => "http://kamal-proxy:80" }
    ], TunnelRoutes.rules("svnmns.com").map { |r| r.transform_keys(&:to_s) }
  end

  test "setup sends the same rules" do
    assert_equal TunnelRoutes.rules("svnmns.com"), CloudflareSetup.new(base_domain: "svnmns.com").send(:ingress)
  end

  # docs/plans/delete-project.md: once removal starts, a project's hosts stop
  # reaching Mission Control (whose sign-in they'd get otherwise).
  test "a project being removed has no routes" do
    equip = make_project("equip", domains: %w[equipping.com])
    equip.update!(maintenance_since: Time.current)
    deletion = ProjectDeletion.create!(project: equip, name: "equip", by: "admin", status: "running", heartbeat_at: Time.current)
    assert_equal %w[equip.svnmns.com equipping.com], maintenance_hosts(TunnelRoutes.rules("svnmns.com").map(&:stringify_keys)), "the final snapshot is still being taken"
    deletion.update!(removing_at: Time.current)
    assert_equal [], maintenance_hosts(TunnelRoutes.rules("svnmns.com").map(&:stringify_keys))
    deletion.update!(status: "no_go")
    assert_equal [], maintenance_hosts(TunnelRoutes.rules("svnmns.com").map(&:stringify_keys)), "stopped partway: still removed"
  end
end
