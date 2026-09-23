require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/tunnel_helpers"

class MaintenanceTest < ActiveSupport::TestCase
  include ProjectHelpers
  include TunnelHelpers

  setup do
    connect_tunnel
    @project = make_project("equip", domains: %w[equipping.com])
  end

  test "turning the maintenance page on and off" do
    pushes = record_pushes
    Maintenance.on!(@project, by: "admin@example.com", message: "Back by 10:00")
    @project.reload
    assert @project.maintenance_since
    assert_equal [ "admin@example.com", "Back by 10:00" ], [ @project.maintenance_by, @project.maintenance_message ]
    assert_equal [ %w[equip.svnmns.com equipping.com] ], pushes.map { |i| maintenance_hosts(i) }

    # On again: pushed again (it repairs a lost route), since kept.
    since = @project.maintenance_since
    travel 5.minutes do
      Maintenance.on!(@project, by: "token agent", message: nil)
    end
    assert_equal since, @project.reload.maintenance_since
    assert_equal 2, pushes.size

    Maintenance.off!(@project)
    @project.reload
    assert_nil @project.maintenance_since
    assert_nil @project.maintenance_by
    assert_equal [], maintenance_hosts(pushes.last)
  end

  test "Cloudflare refusing leaves the state as it was" do
    record_pushes(status: 400, message: "Tunnel configuration is invalid")
    error = assert_raises(Maintenance::Failed) { Maintenance.on!(@project, by: "admin@example.com", message: nil) }
    assert_match "Tunnel configuration is invalid", error.message
    assert_nil @project.reload.maintenance_since, "the database never says on while the tunnel says off"

    @project.update!(maintenance_since: 1.hour.ago, maintenance_by: "admin@example.com")
    assert_raises(Maintenance::Failed) { Maintenance.off!(@project) }
    assert @project.reload.maintenance_since, "still on: the tunnel still routes it"
  end

  test "no tunnel, no maintenance page" do
    Installation.current.update!(tunnel_id: nil)
    pushes = record_pushes
    error = assert_raises(Maintenance::Failed) { Maintenance.on!(@project, by: "admin@example.com", message: nil) }
    assert_match "Cloudflare isn't connected", error.message
    assert_empty pushes
    assert_nil @project.reload.maintenance_since
  end

  test "a message is at most 500 characters" do
    record_pushes
    assert_raises(ActiveRecord::RecordInvalid) { Maintenance.on!(@project, by: "admin@example.com", message: "x" * 501) }
    assert_nil @project.reload.maintenance_since
  end
end

# The pushes are computed from the database under a lock, so two toggles at
# once can't leave the tunnel without one of them. Real threads: no wrapping
# transaction.
class MaintenanceTogglesTest < ActiveSupport::TestCase
  include ProjectHelpers
  include TunnelHelpers
  self.use_transactional_tests = false

  teardown do
    ProjectHost.delete_all
    Project.delete_all
    Installation.current.update!(cloudflare_account_id: nil, tunnel_id: nil, cloudflare_api_token: nil)
  end

  test "two toggles at once" do
    connect_tunnel
    one, two = make_project("one"), make_project("two")
    # The race: one's push has computed its rules (only one) and is on its
    # way to Cloudflare when two commits and pushes. Without the lock, one's
    # older push lands last and two's hostname is dropped from the tunnel.
    pushes = []
    inside = Queue.new
    stub_request(:put, "#{API}/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/configurations").to_return do |request|
      ingress = JSON.parse(request.body).dig("config", "ingress")
      if pushes.empty? && !@signalled
        @signalled = true
        inside << :in_flight
        sleep 0.5
      end
      pushes << ingress
      { status: 200, headers: { "Content-Type" => "application/json" }, body: { success: true, errors: [], messages: [], result: {} }.to_json }
    end
    first = Thread.new { Maintenance.on!(one, by: "admin@example.com", message: nil) }
    inside.pop
    second = Thread.new { Maintenance.on!(two, by: "admin@example.com", message: nil) }
    [ first, second ].each(&:join)
    assert_equal %w[one.svnmns.com two.svnmns.com], maintenance_hosts(pushes.last).sort, "the last push holds both"
  end
end
