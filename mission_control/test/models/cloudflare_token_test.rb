require "test_helper"
require_relative "../support/cloudflare_settings_stubs"
require_relative "../support/project_helpers"

# Replacing Houston's Cloudflare token, checked before it's saved
# (docs/plans/cloudflare-settings.md, rows 5-7).
class CloudflareTokenTest < ActiveSupport::TestCase
  include CloudflareSettingsStubs
  include ProjectHelpers

  NEW = "cf-new-token-456"

  setup do
    connect_host_by_host
    make_project("estherpictures", domains: %w[estherpictures.com www.estherpictures.com])
  end

  # Cloudflare's answers for the new token; a check can be made to fail.
  def as_new(method, path, query: nil, status: 200, result: nil)
    stub = stub_request(method, "#{API}#{path}").with(headers: { "Authorization" => "Bearer #{NEW}" })
    stub = stub.with(query:) if query
    stub.to_return(status:, headers: { "Content-Type" => "application/json" },
                   body: { success: status < 400, errors: status < 400 ? [] : [ { code: 10000, message: "Authentication error" } ], messages: [], result: }.to_json)
  end

  def new_token_sees(accounts: [ { id: ACCOUNT, name: "Seven Moons" } ], tunnel: 200, base: 200, esther: 200, accounts_status: 200)
    as_new(:get, "/accounts", query: { "per_page" => "50" }, status: accounts_status, result: accounts)
    as_new(:get, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}", status: tunnel, result: { id: TUNNEL })
    as_new(:get, "/zones", query: { "name" => "svnmns.com" }, result: [ { id: ZONE, name: "svnmns.com" } ])
    as_new(:get, "/zones/#{ZONE}/dns_records", query: { "per_page" => "1" }, status: base, result: [])
    as_new(:get, "/zones", query: { "name" => "estherpictures.com" }, result: [ { id: OTHER_ZONE, name: "estherpictures.com" } ])
    as_new(:get, "/zones", query: { "name" => "www.estherpictures.com" }, result: [])
    as_new(:get, "/zones/#{OTHER_ZONE}/dns_records", query: { "per_page" => "1" }, status: esther, result: [])
  end

  test "a token that passes every check replaces the old one" do
    new_token_sees
    token = CloudflareToken.new(NEW)
    assert token.replace
    assert token.checks.all?(&:ok)
    assert_equal [ "Account · Seven Moons", "Cloudflare Tunnel · this server's tunnel", "Zone · DNS on svnmns.com", "Zone · DNS on estherpictures.com" ], token.checks.map(&:label)
    assert_equal NEW, Installation.current.reload.cloudflare_api_token
    raw = Installation.connection.select_value("SELECT cloudflare_api_token FROM installations WHERE id = #{Installation.current.id}")
    assert_not_includes raw, NEW, "stored encrypted"
  end

  test "any failed check keeps the old token" do
    {
      "another account" => { accounts: [ { id: "acc-other", name: "Someone else" } ] },
      "rejected" => { accounts_status: 401, accounts: nil },
      "no tunnel access" => { tunnel: 403 },
      "no DNS on the base zone" => { base: 403 },
      "no DNS on a project domain's zone" => { esther: 403 }
    }.each do |what, sees|
      WebMock.reset!
      new_token_sees(**sees)
      token = CloudflareToken.new(NEW)
      assert_not token.replace, what
      assert token.checks.any? { |c| !c.ok }, "#{what}: a failed check is shown"
      assert_equal TOKEN, Installation.current.reload.cloudflare_api_token, "#{what}: the old token stays"
    end
    assert_not CloudflareToken.new("  ").replace, "blank"
  end

  test "the token is never shown or logged" do
    new_token_sees
    io = StringIO.new
    original, Rails.logger = Rails.logger, ActiveSupport::Logger.new(io)
    token = CloudflareToken.new(NEW)
    token.replace
    assert_not_includes token.checks.map(&:label).join, NEW
    assert_not_includes io.string, NEW
  ensure
    Rails.logger = original
  end
end
