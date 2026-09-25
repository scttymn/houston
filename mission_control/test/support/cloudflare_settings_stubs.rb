require_relative "tunnel_helpers"

# A connected installation, host by host, and Cloudflare's answers for the
# Settings view (docs/plans/cloudflare-settings.md).
module CloudflareSettingsStubs
  include TunnelHelpers

  OTHER_ZONE = "zone-esther"

  def connect_host_by_host
    Installation.current.update!(cloudflare_account_id: ACCOUNT, tunnel_id: TUNNEL, cloudflare_api_token: TOKEN,
                                 cloudflare_zone_id: ZONE, dns_mode: "per_host")
  end

  def here(name) = "#{TUNNEL}.cfargotunnel.com"

  def stub_tunnel_details(connections: 4, status: 200)
    conns = (1..connections).map { |i| { id: "c#{i}", colo_name: %w[mci01 dfw06 mci03 dfw01][i - 1], origin_ip: "99.98.226.252",
                                         client_version: "2026.9.1", opened_at: "2026-09-24T20:10:0#{i}Z", is_pending_reconnect: false } }
    cf(:get, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}", status:,
       result: status < 400 ? { id: TUNNEL, name: "houston-svnmns", status: "healthy", created_at: "2026-09-23T04:00:00Z", connections: conns } : nil,
       errors: status < 400 ? [] : [ { code: 1000, message: "Authentication error" } ])
  end

  def stub_live_ingress(ingress = expected_ingress[:config][:ingress])
    cf(:get, "/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/configurations", result: { config: { ingress: } })
  end

  def stub_zones(zones = [ { id: ZONE, name: "svnmns.com" }, { id: OTHER_ZONE, name: "estherpictures.com" } ])
    cf(:get, "/zones", query: { "per_page" => "50" }, result: zones)
  end

  def stub_records(zone, records)
    cf(:get, "/zones/#{zone}/dns_records", query: { "per_page" => "100", "page" => "1" }, result: records)
  end

  def record(name, comment:, content: "#{TUNNEL}.cfargotunnel.com", id: "r-#{name}")
    { id:, type: "CNAME", name:, content:, proxied: true, comment: }
  end
end
