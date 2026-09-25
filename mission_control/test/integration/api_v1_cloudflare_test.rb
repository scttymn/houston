require "test_helper"
require_relative "../support/cloudflare_settings_stubs"

# The Cloudflare view, token and repair over the remote API
# (docs/plans/cloudflare-settings.md, rows 9 and 10).
class ApiV1CloudflareTest < ActionDispatch::IntegrationTest
  include CloudflareSettingsStubs

  setup do
    connect_host_by_host
    @token, = ApiToken.issue!("laptop")
  end

  def auth = { "Authorization" => "Bearer #{@token}" }
  def json = response.parsed_body

  test "the view, the token and repair" do
    stub_tunnel_details
    stub_live_ingress
    stub_zones([ { id: ZONE, name: "svnmns.com" } ])
    stub_records(ZONE, [])
    get "/api/v1/cloudflare", headers: auth
    assert_response :success
    assert_equal [ "houston-svnmns", 4, false ], [ json.dig("tunnel", "name"), json["connections"].size, json["drift"] ]

    stub_request(:get, "#{API}/accounts?per_page=50").with(headers: { "Authorization" => "Bearer cf-bad" })
      .to_return(status: 401, headers: { "Content-Type" => "application/json" }, body: { success: false, errors: [ { code: 1000, message: "Invalid API Token" } ], result: nil }.to_json)
    put "/api/v1/cloudflare/token", params: { token: "cf-bad" }.to_json, headers: auth.merge("Content-Type" => "application/json")
    assert_response :unprocessable_entity
    assert_equal false, json["replaced"]
    assert_match(/isn't valid/, json["checks"].first["label"])
    assert_not_includes response.body, "cf-bad"

    record_pushes
    stub_request(:get, %r{#{API}/zones/#{ZONE}/dns_records\?name=}).to_return(headers: { "Content-Type" => "application/json" }, body: { success: true, errors: [], result: [] }.to_json)
    stub_request(:post, %r{#{API}/zones/#{ZONE}/dns_records}).to_return(headers: { "Content-Type" => "application/json" }, body: { success: true, errors: [], result: {} }.to_json)
    post "/api/v1/cloudflare/repair", headers: auth
    assert_response :success
    assert_equal "OK", json["results"].first["state"]
  end

  test "the API needs a token" do
    get "/api/v1/cloudflare"
    assert_response :unauthorized
    put "/api/v1/cloudflare/token", params: { token: "x" }.to_json, headers: { "Content-Type" => "application/json" }
    assert_response :unauthorized
    post "/api/v1/cloudflare/repair"
    assert_response :unauthorized
    assert_equal TOKEN, Installation.current.reload.cloudflare_api_token
  end
end
