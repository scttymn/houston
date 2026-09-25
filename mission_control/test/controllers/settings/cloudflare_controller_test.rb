require "test_helper"
require_relative "../../support/cloudflare_settings_stubs"
require_relative "../../support/project_helpers"

# Settings › Cloudflare (docs/plans/cloudflare-settings.md, rows 4, 7, 9 and the UI).
class Settings::CloudflareControllerTest < ActionDispatch::IntegrationTest
  include CloudflareSettingsStubs
  include ProjectHelpers

  setup do
    connect_host_by_host
    sign_in_as users(:one)
  end

  test "the page doesn't wait on Cloudflare" do
    stub_tunnel_details # the header's cached status only; nothing else is stubbed, so any other call fails
    get settings_path
    assert_response :success
    assert_select "section#cloudflare turbo-frame#cloudflare-live[src='#{settings_cloudflare_path}'][loading=lazy]", 1
  end

  test "the live panel: the tunnel, its connections, the routes and Houston's records" do
    stub_tunnel_details
    stub_live_ingress
    stub_zones
    stub_records(ZONE, [ record("equip.svnmns.com", comment: "managed-by:houston project:equip"),
                         record("other.svnmns.com", comment: "managed-by:houston project:other", content: "ffff-second.cfargotunnel.com") ])
    stub_records(OTHER_ZONE, [])
    get settings_cloudflare_path
    assert_response :success
    assert_select "turbo-frame#cloudflare-live" do
      assert_select ".cf-tunnel", /houston-svnmns.*healthy/m
      assert_select ".cf-tunnel [data-clipboard-target=source]", TUNNEL
      assert_select ".cf-connections li", 4
      assert_select ".cf-connections li", /MCI01.*2026\.9\.1.*99\.98\.226\.252/m
      assert_select ".cf-routes li", 4
      assert_select ".cf-routes .state--nogo", 0, "no drift"
      assert_select ".cf-records li", 2
      assert_select ".cf-records li", /equip\.svnmns\.com.*equip.*this server/m
      assert_select ".cf-records li", /other\.svnmns\.com.*other.*another server/m
    end
  end

  test "a failing Cloudflare fills the panel with a reason" do
    stub_tunnel_details(status: 403)
    stub_request(:get, %r{#{API}/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/configurations}).to_timeout
    stub_zones([])
    get settings_cloudflare_path
    assert_response :success
    assert_select "turbo-frame#cloudflare-live .cf-problems", /tunnel: Authentication error/
  end

  test "replacing the token: a password field; refused keeps the old one; accepted replaces it" do
    stub_tunnel_details
    get settings_path
    assert_select "form[action='#{token_settings_cloudflare_path}'] input[type=password][name=api_token][autocomplete=off]", 1

    stub_request(:get, "#{API}/accounts?per_page=50").with(headers: { "Authorization" => "Bearer cf-bad" })
      .to_return(status: 401, headers: { "Content-Type" => "application/json" }, body: { success: false, errors: [ { code: 1000, message: "Invalid API Token" } ], result: nil }.to_json)
    patch token_settings_cloudflare_path, params: { api_token: "cf-bad" }
    assert_response :unprocessable_entity
    assert_select "section#cloudflare .check", /isn't valid/
    assert_not_includes response.body, "cf-bad", "the token isn't echoed"
    assert_equal TOKEN, Installation.current.reload.cloudflare_api_token
  end

  test "repair lists what it did" do
    stub_tunnel_details
    record_pushes
    stub_request(:get, %r{#{API}/zones/#{ZONE}/dns_records\?name=}).to_return(headers: { "Content-Type" => "application/json" }, body: { success: true, errors: [], result: [] }.to_json)
    stub_request(:post, %r{#{API}/zones/#{ZONE}/dns_records}).to_return(headers: { "Content-Type" => "application/json" }, body: { success: true, errors: [], result: {} }.to_json)
    post repair_settings_cloudflare_path
    assert_response :success
    assert_select "section#cloudflare .cf-repair li", /routes.*OK/m
    assert_select "section#cloudflare .cf-repair li", /admin\.svnmns\.com.*DNS OK/m
  end

  test "signed out, nothing changes" do
    sign_out
    get settings_cloudflare_path
    assert_redirected_to sign_in_path
    patch token_settings_cloudflare_path, params: { api_token: "cf-new" }
    assert_redirected_to sign_in_path
    post repair_settings_cloudflare_path
    assert_redirected_to sign_in_path
    assert_equal TOKEN, Installation.current.reload.cloudflare_api_token
  end
end
