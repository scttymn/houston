require "test_helper"
require_relative "../../support/cloudflare_stubs"

class Setup::CloudflareControllerTest < ActionDispatch::IntegrationTest
  include CloudflareStubs

  setup do
    Installation.delete_all
    sign_in_as users(:one)
  end

  def submit(base_domain: "svnmns.com", api_token: TOKEN)
    post setup_cloudflare_path, params: { cloudflare: { base_domain:, api_token: } }
  end

  def assert_nothing_written
    assert_not_requested :post, /#{API}/
    assert_not_requested :put, /#{API}/
    assert_not_requested :patch, /#{API}/
    assert_not_requested :delete, /#{API}/
    assert_not Installation.connected?
  end

  test "shows the Cloudflare form" do
    get setup_cloudflare_path

    assert_response :success
    assert_select "h1", /Connect Cloudflare/i
    assert_select "input[name='cloudflare[base_domain]']"
    assert_select "input[name='cloudflare[api_token]'][type=password]"
    assert_select "aside", /What Houston will create/i
  end

  test "creates the tunnel, ingress and wildcard" do
    stub_token_checks
    stub_existing_tunnel([])
    tunnel = cf(:post, "/accounts/#{ACCOUNT}/cfd_tunnel", result: { id: TUNNEL, name: "houston-svnmns" })
    cf(:get, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/token", result: "tunnel-token-xyz")
    ingress = cf(:put, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/configurations", result: {})
    stub_existing_wildcard([])
    wildcard = cf(:post, "/zones/#{ZONE}/dns_records", result: { id: "rec1" })

    submit

    assert_redirected_to root_path
    assert_requested tunnel.with(body: { name: "houston-svnmns", config_src: "cloudflare" })
    assert_requested ingress.with(body: expected_ingress)
    assert_requested wildcard.with(body: expected_wildcard)

    installation = Installation.current
    assert installation.connected?
    assert_equal [ "svnmns.com", ACCOUNT, ZONE, TUNNEL ], [ installation.base_domain, installation.cloudflare_account_id, installation.cloudflare_zone_id, installation.tunnel_id ]
    assert_equal TOKEN, installation.cloudflare_api_token
    assert_equal "tunnel-token-xyz", installation.tunnel_token
    raw = Installation.connection.select_one("SELECT cloudflare_api_token, tunnel_token FROM installations")
    assert_not_includes raw.values.join, TOKEN, "the API token is stored encrypted"
    assert_not_includes raw.values.join, "tunnel-token-xyz", "the tunnel token is stored encrypted"
  end

  test "a failed token check creates nothing" do
    [
      [ { accounts_status: 401, accounts: nil }, /isn't valid/ ],
      [ { accounts: [] }, /can't see any Cloudflare account/ ],
      [ { accounts: [ { id: ACCOUNT, name: "A" }, { id: "acc2", name: "B" } ] }, /more than one account/ ],
      [ { tunnels_status: 403 }, /Cloudflare Tunnel/ ],
      [ { zones: [] }, /svnmns\.com isn't one of the token's zones/ ]
    ].each do |options, message|
      WebMock.reset!
      stub_token_checks(**options)

      submit

      assert_response :unprocessable_entity, "#{options} should fail the check"
      assert_select ".check", message
      assert_select ".check .mono", /NO-GO/
      assert_nothing_written
    end
  end

  test "the base domain must be a domain" do
    [ "localhost", "https://svnmns.com", "*.svnmns.com", "" ].each do |bad|
      submit(base_domain: bad)

      assert_response :unprocessable_entity, "#{bad.inspect} should be rejected"
      assert_not_requested :any, /#{API}/
    end
  end

  test "a rerun reuses what exists" do
    stub_token_checks
    stub_existing_tunnel([ { id: TUNNEL, name: "houston-svnmns" } ])
    cf(:get, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/token", result: "tunnel-token-xyz")
    cf(:put, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/configurations", result: {})
    stub_existing_wildcard([ { id: "rec1", name: "*.svnmns.com", content: "old-tunnel.cfargotunnel.com", comment: "managed-by:houston" } ])
    update = cf(:patch, "/zones/#{ZONE}/dns_records/rec1", result: { id: "rec1" })

    submit

    assert_redirected_to root_path
    assert_not_requested :post, "#{API}/accounts/#{ACCOUNT}/cfd_tunnel"
    assert_requested update.with(body: expected_wildcard)
    assert Installation.connected?
  end

  test "never touches a DNS record Houston didn't create" do
    stub_token_checks
    stub_existing_tunnel([ { id: TUNNEL, name: "houston-svnmns" } ])
    cf(:get, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/token", result: "tunnel-token-xyz")
    cf(:put, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/configurations", result: {})
    stub_existing_wildcard([ { id: "theirs", name: "*.svnmns.com", content: "somewhere.example.net", comment: nil } ])

    submit

    assert_response :unprocessable_entity
    assert_select ".check", /\*\.svnmns\.com.*Houston didn't create/
    assert_not_requested :patch, /dns_records/
    assert_not_requested :delete, /dns_records/
    assert_not Installation.connected?
  end

  test "a Cloudflare error mid-way can be retried" do
    stub_token_checks
    stub_existing_tunnel([])
    cf(:post, "/accounts/#{ACCOUNT}/cfd_tunnel", result: { id: TUNNEL, name: "houston-svnmns" })
    cf(:get, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/token", result: "tunnel-token-xyz")
    cf(:put, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/configurations", status: 500, errors: [ { code: 1002, message: "Internal error" } ])

    submit

    assert_response :unprocessable_entity
    assert_select ".check", /Internal error/
    assert_not Installation.connected?

    WebMock.reset!
    stub_token_checks
    stub_existing_tunnel([ { id: TUNNEL, name: "houston-svnmns" } ])
    cf(:get, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/token", result: "tunnel-token-xyz")
    cf(:put, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/configurations", result: {})
    stub_existing_wildcard([])
    cf(:post, "/zones/#{ZONE}/dns_records", result: { id: "rec1" })

    submit

    assert_redirected_to root_path
    assert Installation.connected?
  end

  test "the step closes once connected" do
    Installation.create!(base_domain: "svnmns.com", cloudflare_connected_at: Time.current)

    get setup_cloudflare_path

    assert_redirected_to root_path
  end

  test "hands the tunnel token to cloudflared" do
    stub_token_checks
    stub_existing_tunnel([ { id: TUNNEL, name: "houston-svnmns" } ])
    cf(:get, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/token", result: "tunnel-token-xyz")
    cf(:put, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/configurations", result: {})
    stub_existing_wildcard([])
    cf(:post, "/zones/#{ZONE}/dns_records", result: { id: "rec1" })

    Dir.mktmpdir do |dir|
      path = File.join(dir, "tunnel-token")
      with_env("HOUSTON_TUNNEL_TOKEN_PATH" => path) { submit }

      assert_redirected_to root_path
      assert_equal "tunnel-token-xyz", File.read(path)
      assert_equal "600", format("%o", File.stat(path).mode & 0o777)
    end
  end

  test "no token file without a path" do
    stub_token_checks
    stub_existing_tunnel([ { id: TUNNEL, name: "houston-svnmns" } ])
    cf(:get, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/token", result: "tunnel-token-xyz")
    cf(:put, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/configurations", result: {})
    stub_existing_wildcard([])
    cf(:post, "/zones/#{ZONE}/dns_records", result: { id: "rec1" })

    with_env("HOUSTON_TUNNEL_TOKEN_PATH" => nil) { submit }

    assert_redirected_to root_path
  end

  private
    def with_env(vars)
      saved = vars.keys.to_h { |k| [ k, ENV[k] ] }
      vars.each { |k, v| v.nil? ? ENV.delete(k) : ENV[k] = v }
      yield
    ensure
      saved.each { |k, v| v.nil? ? ENV.delete(k) : ENV[k] = v }
    end
end
