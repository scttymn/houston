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

  def stub_everything
    stub_tunnel_details
    stub_live_ingress
    stub_zones
    stub_records(ZONE, [ record("hooks.svnmns.com", comment: "managed-by:houston"),
                         record("equip.svnmns.com", comment: "managed-by:houston project:equip"),
                         record("admin.svnmns.com", comment: "managed-by:houston"),
                         record("other.svnmns.com", comment: "managed-by:houston project:other", content: "ffff-second.cfargotunnel.com") ])
    stub_records(OTHER_ZONE, [])
  end

  # Each element's text; a table row's cells joined by spaces.
  def texts(selector) = css_select(selector).map { |e| (e.css("td").any? ? e.css("td").map(&:text).join(" ") : e.text).squish }

  test "the page doesn't wait on Cloudflare: it shows the last answer, and asks again when it's old" do
    Rails.cache.clear
    stub_tunnel_details # the header's cached status only; nothing else is stubbed, so any other call fails
    get settings_path
    assert_response :success
    assert_select "section#cloudflare turbo-frame#cloudflare-live[src='#{settings_cloudflare_path}'][loading=lazy]", /Asking Cloudflare/

    stub_everything
    CloudflareView.fetch
    WebMock.reset!
    stub_local_services
    stub_tunnel_details
    get settings_path
    assert_select "turbo-frame#cloudflare-live:not([src])" do
      assert_select ".cf-checked", /Checked less than a minute ago/
      assert_select "table.cf-connections tbody tr", 4
    end

    travel CloudflareView::STALE_AFTER + 1.minute
    get settings_path
    assert_select "turbo-frame#cloudflare-live[src='#{settings_cloudflare_path}'][loading=lazy] table.cf-connections tbody tr", 4, "old: shown, and asked again"
  ensure
    Rails.cache.clear
  end

  test "the live panel: the tunnel, its connections, the routes and Houston's records, with headings" do
    stub_everything
    get settings_cloudflare_path
    assert_response :success
    assert_select "turbo-frame#cloudflare-live" do
      assert_select ".cf-checked", /Checked less than a minute ago/
      assert_select ".cf-checked a[href='#{settings_cloudflare_path}'][data-turbo-frame='cloudflare-live']", "Check now"
      assert_equal [ "Name", "Created", "Tunnel ID" ], texts(".cf-tunnel dt")
      assert_select ".cf-tunnel dd", /houston-svnmns.*healthy/m
      assert_select ".cf-tunnel [data-clipboard-target=source]", TUNNEL
      assert_select ".cf-tunnel button[aria-label='Copy tunnel ID']", "Copy"
      assert_equal [ "Data center", "cloudflared", "From", "Connected" ], texts("table.cf-connections th")
      assert_select "table.cf-connections tbody tr", 4
      assert_select "table.cf-connections tbody tr", /MCI01.*2026\.9\.1.*99\.98\.226\.252/m
      assert_equal [ "Hostname", "Path", "Goes to" ], texts("table.cf-routes th")
      assert_equal [ "admin.svnmns.com any Mission Control http://mission-control:80",
                     "hooks.svnmns.com /<project> Webhooks http://mission-control:80",
                     "hooks.svnmns.com anything else Nothing (404)",
                     "Every other name any Your apps http://kamal-proxy:80" ], texts("table.cf-routes tbody tr")
      assert_select "table.cf-routes .state--nogo", 0, "no drift"
      assert_equal [ "Name", "For", "Points at", "Proxy" ], texts("table.cf-records th")
      assert_equal [ "admin.svnmns.com Mission Control this server proxied",
                     "hooks.svnmns.com Webhooks this server proxied",
                     "equip.svnmns.com equip this server proxied",
                     "other.svnmns.com other another server proxied" ], texts("table.cf-records tbody tr")
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

  # One row: what the token needs, "More" with every permission and where to
  # make it (the same for anyone's Houston: no domain names), then the
  # field and one button. Refused keeps the old one.
  test "replacing the token" do
    stub_tunnel_details
    Installation.current.update!(base_domain: "example.test")
    get settings_path
    assert_select "section#cloudflare .cf-token-row" do
      assert_select ".cf-row__text", /Account · Cloudflare Tunnel · Edit.*Zone · DNS · Edit/m
      assert_select "details.cf-token-help summary", "More"
      assert_select "details.cf-token-help a[href='https://dash.cloudflare.com/profile/api-tokens']"
      assert_select "details.cf-token-help", /All zones/
      assert_select "details.cf-token-help", /example\.test/, "the base domain configured here"
      assert_select "form[action='#{token_settings_cloudflare_path}'] input[type=password][name=api_token][autocomplete=off]", 1
      assert_equal [ "Check and replace" ], css_select(".cf-token-row button, .cf-token-row input[type=submit]").map { |b| (b["value"] || b.text).squish }
    end
    assert_no_match(/svnmns|estherpictures/, css_select(".cf-token-row .cf-row__text, details.cf-token-help").map(&:text).join, "no other domains in the help")

    stub_request(:get, "#{API}/accounts?per_page=50").with(headers: { "Authorization" => "Bearer cf-bad" })
      .to_return(status: 401, headers: { "Content-Type" => "application/json" }, body: { success: false, errors: [ { code: 1000, message: "Invalid API Token" } ], result: nil }.to_json)
    patch token_settings_cloudflare_path, params: { api_token: "cf-bad" }
    assert_response :unprocessable_entity
    assert_select "section#cloudflare .cf-token-row .check", /isn't valid/
    assert_select "section#cloudflare .cf-token-row", /The old token is still in use/
    assert_not_includes response.body, "cf-bad", "the token isn't echoed"
    assert_equal TOKEN, Installation.current.reload.cloudflare_api_token
  end

  test "repair lists what it did" do
    stub_tunnel_details
    record_pushes
    stub_request(:get, %r{#{API}/zones/#{ZONE}/dns_records\?name=}).to_return(headers: { "Content-Type" => "application/json" }, body: { success: true, errors: [], result: [] }.to_json)
    stub_request(:post, %r{#{API}/zones/#{ZONE}/dns_records}).to_return(headers: { "Content-Type" => "application/json" }, body: { success: true, errors: [], result: {} }.to_json)
    Rails.cache.write([ "cloudflare-view", TUNNEL ], { "view" => CloudflareView.new(Installation.current).to_h.as_json, "checked_at" => Time.current.iso8601 })
    get settings_path
    assert_select "section#cloudflare .cf-repair-row .cf-row__text", /puts .* back/
    assert_select "section#cloudflare .cf-repair-row form[action='#{repair_settings_cloudflare_path}'] button", "Repair routes and records"

    post repair_settings_cloudflare_path
    assert_response :success
    assert_equal [ "Item", "Result", "Details" ], texts("section#cloudflare .cf-repair-row table.cf-repair th")
    assert_select "section#cloudflare .cf-repair-row table.cf-repair tbody tr", /routes.*OK/m
    assert_select "section#cloudflare .cf-repair-row table.cf-repair tbody tr", /admin\.svnmns\.com.*DNS OK/m
    assert_nil CloudflareView.last, "the next look asks Cloudflare again"
  ensure
    Rails.cache.clear
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
