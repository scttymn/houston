require "webmock/minitest"

# Stubs Cloudflare's v4 API for the Cloudflare setup step. Every stub requires
# the bearer token, so a request without it fails loudly.
module CloudflareStubs
  API = "https://api.cloudflare.com/client/v4"
  TOKEN = "cf-test-token-123"
  ACCOUNT = "acc123"
  ZONE = "zone123"
  TUNNEL = "5c1f0e2a-tunnel"

  def cf(method, path, query: nil, status: 200, result: nil, errors: [])
    stub = stub_request(method, "#{API}#{path}").with(headers: { "Authorization" => "Bearer #{TOKEN}" })
    stub = stub.with(query: query) if query
    stub.to_return(status:, headers: { "Content-Type" => "application/json" },
                   body: { success: status < 400, errors:, messages: [], result: }.to_json)
  end

  def stub_token_checks(accounts: [ { id: ACCOUNT, name: "Seven Moons" } ], tunnels_status: 200, zones: [ { id: ZONE, name: "svnmns.com" } ], accounts_status: 200)
    cf(:get, "/accounts", query: { "per_page" => "50" }, status: accounts_status, result: accounts,
       errors: accounts_status == 401 ? [ { code: 1000, message: "Invalid API Token" } ] : [])
    cf(:get, "/accounts/#{ACCOUNT}/cfd_tunnel", query: { "per_page" => "1" }, status: tunnels_status, result: [],
       errors: tunnels_status == 403 ? [ { code: 10000, message: "Authentication error" } ] : [])
    cf(:get, "/zones", query: { "name" => "svnmns.com" }, result: zones)
    cf(:get, "/zones/#{ZONE}/dns_records", query: { "per_page" => "1" }, result: [])
  end

  def stub_existing_tunnel(result)
    cf(:get, "/accounts/#{ACCOUNT}/cfd_tunnel", query: { "name" => "houston-svnmns", "is_deleted" => "false" }, result:)
  end

  def stub_existing_wildcard(result)
    cf(:get, "/zones/#{ZONE}/dns_records", query: { "type" => "CNAME", "name" => "*.svnmns.com" }, result:)
  end

  def expected_ingress
    { config: { ingress: [
      { hostname: "admin.svnmns.com", service: "http://mission-control:80" },
      { hostname: "hooks.svnmns.com", path: "^/[a-z0-9-]+$", service: "http://mission-control:80" },
      { hostname: "hooks.svnmns.com", service: "http_status:404" },
      { service: "http://kamal-proxy:80" }
    ] } }
  end

  def expected_wildcard
    { type: "CNAME", name: "*.svnmns.com", content: "#{TUNNEL}.cfargotunnel.com", proxied: true, comment: "managed-by:houston" }
  end
end
