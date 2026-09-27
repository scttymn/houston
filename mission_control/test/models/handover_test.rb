require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/tunnel_helpers"

# The hosts a copy's old and new projects share, moved to the new one with
# no failed request (docs/plans/copy-project.md, spike notes): a catch-all to
# the new container while kamal-proxy's hosts move, then their DNS records'
# owner. And back, for Undo copy.
class HandoverTest < ActiveSupport::TestCase
  include ProjectHelpers
  include FakeDockerHelper
  include TunnelHelpers

  OLD_TARGET = "91f46ac53058:80"
  NEW_TARGET = "6a8c0ce49a59:80"
  STATE = "/home/kamal-proxy/.config/kamal-proxy/kamal-proxy.state"

  setup do
    connect_tunnel
    Installation.current.update!(cloudflare_zone_id: ZONE)
    @old = make_project("equip-go", domains: %w[equip.svnmns.com equipping.com])
    @new = make_project("equip", domains: %w[equipping.com])
    @copy = ProjectCopy.create!(from_project: @old, project: @new, from: "equip-go", to: "equip", sha: "a" * 40, by: "admin", status: "running")
  end

  def service(name, hosts, target, health: "/up")
    { name:, options: { hosts:, path_prefixes: [ "/" ], tls_enabled: false },
      target_options: { health_check_config: { path: health, port: 0, interval: 1_000_000_000, timeout: 5_000_000_000, host: "" },
                        response_timeout: 30_000_000_000, buffer_requests: true, buffer_responses: true, max_memory_buffer_size: 1_048_576,
                        max_request_body_size: 0, max_response_body_size: 0, log_request_headers: %w[Cache-Control User-Agent],
                        log_response_headers: nil, forward_headers: true },
      active_targets: [ target ] }
  end

  def proxy(state = [ service("equip-go-web", %w[equip-go.svnmns.com equip.svnmns.com equipping.com], OLD_TARGET),
                      service("equip-web", [ "equip.houston-copy.invalid" ], NEW_TARGET, health: "/health") ], moved: true)
    FakeDocker.new do |args, _env|
      if args == [ "exec", "kamal-proxy", "cat", STATE ] then DockerCommand::Result.new(success: true, output: state.to_json)
      elsif args[0..2] == [ "exec", "kamal-proxy", "sh" ] && !moved then failure("Error: host settings conflict with another service")
      end
    end
  end

  def script(fake) = fake.calls.find { |c| c.args[0..2] == [ "exec", "kamal-proxy", "sh" ] }&.args&.last

  # The records Houston keeps for the shared hosts, owned by the old project.
  def stub_records
    cf(:get, "/zones", query: { "name" => "equipping.com" }, result: [ { id: "zone-eq", name: "equipping.com", status: "active" } ])
    { [ ZONE, "equip.svnmns.com" ] => "r1", [ "zone-eq", "equipping.com" ] => "r2" }.each do |(zone, name), id|
      cf(:get, "/zones/#{zone}/dns_records", query: { "name" => name }, result: [ { id:, name:, type: "CNAME", comment: "managed-by:houston project:equip-go" } ])
      cf(:patch, "/zones/#{zone}/dns_records/#{id}", result: { id: })
    end
  end

  test "the shared hosts move to the new project, with a catch-all while they do" do
    stub_records
    fake = proxy
    assert_equal %w[equip.svnmns.com equipping.com], use_fake_docker(fake) { Handover.new(@copy).forward! }

    moves = script(fake).split(" && ")
    assert_equal 4, moves.size
    assert_match %r{\Akamal-proxy deploy houston-handover --target #{NEW_TARGET} (?!.*--host)}, moves[0]
    assert_match "--health-check-path /health", moves[0], "the catch-all checks the new app as its own service does"
    assert_match %r{\Akamal-proxy deploy equip-go-web --target #{OLD_TARGET} --host equip-go\.svnmns\.com --health-check-path /up}, moves[1]
    assert_no_match(/equipping\.com|--host equip\.svnmns/, moves[1])
    assert_match %r{\Akamal-proxy deploy equip-web --target #{NEW_TARGET} --host equip\.svnmns\.com --host equipping\.com --health-check-path /health}, moves[2]
    assert_no_match(/houston-copy\.invalid/, moves[2], "the placeholder goes once the new project has hosts")
    assert_equal "kamal-proxy remove houston-handover", moves[3]
    [ moves[1], moves[2] ].each do |move|
      assert_match "--health-check-interval 1000ms --health-check-timeout 5000ms --target-timeout 30000ms --buffer-requests --buffer-responses", move
      assert_match "--log-request-header Cache-Control --log-request-header User-Agent --forward-headers=true", move
      assert_no_match(/--force/, move, "never --force: it skips the health check and answers 503s (spike notes)")
    end

    assert_requested :patch, "#{API}/zones/#{ZONE}/dns_records/r1", body: { comment: "managed-by:houston project:equip" }.to_json
    assert_requested :patch, "#{API}/zones/zone-eq/dns_records/r2", body: { comment: "managed-by:houston project:equip" }.to_json
    assert_equal [ %w[equip.svnmns.com equipping.com], true ], [ @copy.reload.handed_over, @copy.handed_over_at.present? ]
  end

  test "an old project left with no host of its own keeps a placeholder, never a catch-all" do
    stub_records
    fake = proxy([ service("equip-go-web", %w[equip.svnmns.com equipping.com], OLD_TARGET), service("equip-web", [ "equip.houston-copy.invalid" ], NEW_TARGET) ])
    use_fake_docker(fake) { Handover.new(@copy).forward! }
    assert_match %r{kamal-proxy deploy equip-go-web --target #{OLD_TARGET} --host equip-go\.houston-copy\.invalid }, script(fake)
  end

  test "nothing shared: nothing moves" do
    fake = proxy([ service("equip-go-web", %w[equip-go.svnmns.com], OLD_TARGET), service("equip-web", [ "equip.houston-copy.invalid" ], NEW_TARGET) ])
    @new.update!(domains: [])
    @old.update!(domains: [])
    assert_equal [], use_fake_docker(fake) { Handover.new(@copy).forward! }
    moves = script(fake).split(" && ")
    assert_equal 1, moves.size, "only the placeholder comes off the new service"
    assert_match %r{\Akamal-proxy deploy equip-web --target #{NEW_TARGET} --host equip\.svnmns\.com }, moves[0]
  end

  test "a move kamal-proxy refuses is put back, and nothing else is touched" do
    fake = proxy(moved: false)
    error = assert_raises(Handover::Failed) { use_fake_docker(fake) { Handover.new(@copy).forward! } }
    assert_match "host settings conflict", error.message
    back = fake.calls.map(&:args).select { |a| a[0..2] == [ "exec", "kamal-proxy", "sh" ] }.last.last
    assert_match %r{kamal-proxy remove houston-handover}, back
    assert_match %r{kamal-proxy deploy equip-go-web --target #{OLD_TARGET} --host equip-go\.svnmns\.com --host equip\.svnmns\.com --host equipping\.com }, back
    assert_not_requested :patch, %r{dns_records}
    assert_nil @copy.reload.handed_over_at
  end

  test "a DNS record that can't be moved puts the hosts back" do
    stub_records
    cf(:patch, "/zones/zone-eq/dns_records/r2", status: 403, errors: [ { code: 10000, message: "Authentication error" } ])
    fake = proxy
    error = assert_raises(Handover::Failed) { use_fake_docker(fake) { Handover.new(@copy).forward! } }
    assert_match "Authentication error", error.message
    scripts = fake.calls.map(&:args).select { |a| a[0..2] == [ "exec", "kamal-proxy", "sh" ] }.map(&:last)
    assert_equal 2, scripts.size, "forward, then back"
    assert_match %r{kamal-proxy deploy equip-go-web --target #{OLD_TARGET} --host equip-go\.svnmns\.com --host equip\.svnmns\.com --host equipping\.com }, scripts.last
    assert_requested :patch, "#{API}/zones/#{ZONE}/dns_records/r1", body: { comment: "managed-by:houston project:equip-go" }.to_json
  end

  test "a record that isn't the old project's is left alone" do
    cf(:get, "/zones", query: { "name" => "equipping.com" }, result: [ { id: "zone-eq", name: "equipping.com", status: "active" } ])
    cf(:get, "/zones/#{ZONE}/dns_records", query: { "name" => "equip.svnmns.com" }, result: [])
    cf(:get, "/zones/zone-eq/dns_records", query: { "name" => "equipping.com" }, result: [ { id: "r2", name: "equipping.com", comment: "someone else's" } ])
    use_fake_docker(proxy) { Handover.new(@copy).forward! }
    assert_not_requested :patch, %r{dns_records}
  end

  test "undo: the hosts go back to the old project" do
    stub_records
    { [ ZONE, "equip.svnmns.com" ] => "r1", [ "zone-eq", "equipping.com" ] => "r2" }.each do |(zone, name), id|
      cf(:get, "/zones/#{zone}/dns_records", query: { "name" => name }, result: [ { id:, name:, type: "CNAME", comment: "managed-by:houston project:equip" } ])
    end
    @copy.update!(status: "go", handed_over: %w[equip.svnmns.com equipping.com], handed_over_at: Time.current)
    fake = proxy([ service("equip-go-web", %w[equip-go.svnmns.com], OLD_TARGET), service("equip-web", %w[equip.svnmns.com equipping.com], NEW_TARGET) ])
    use_fake_docker(fake) { Handover.new(@copy).back! }

    moves = script(fake).split(" && ")
    assert_match %r{\Akamal-proxy deploy houston-handover --target #{OLD_TARGET} }, moves[0]
    assert_match %r{\Akamal-proxy deploy equip-web --target #{NEW_TARGET} --host equip\.houston-copy\.invalid }, moves[1]
    assert_match %r{\Akamal-proxy deploy equip-go-web --target #{OLD_TARGET} --host equip-go\.svnmns\.com --host equip\.svnmns\.com --host equipping\.com }, moves[2]
    assert_requested :patch, "#{API}/zones/#{ZONE}/dns_records/r1", body: { comment: "managed-by:houston project:equip-go" }.to_json
    assert_equal [ [], nil ], [ @copy.reload.handed_over, @copy.handed_over_at ]
  end
end
