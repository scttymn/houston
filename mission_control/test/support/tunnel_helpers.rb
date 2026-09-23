require_relative "cloudflare_stubs"

# The tunnel's configuration pushes, recorded: each PUT's ingress rules.
module TunnelHelpers
  include CloudflareStubs

  def connect_tunnel
    Installation.current.update!(cloudflare_account_id: ACCOUNT, tunnel_id: TUNNEL, cloudflare_api_token: TOKEN)
  end

  # Returns the list each push's ingress is appended to; status: what Cloudflare answers.
  def record_pushes(status: 200, message: "Tunnel configuration is invalid")
    pushes = []
    stub_request(:put, "#{API}/accounts/#{ACCOUNT}/cfd_tunnel/#{TUNNEL}/configurations")
      .with(headers: { "Authorization" => "Bearer #{TOKEN}" })
      .to_return do |request|
        pushes << JSON.parse(request.body).dig("config", "ingress")
        { status:, headers: { "Content-Type" => "application/json" },
          body: { success: status < 400, errors: status < 400 ? [] : [ { code: 1000, message: } ], messages: [], result: {} }.to_json }
      end
    pushes
  end

  def maintenance_hosts(ingress) = ingress.select { |r| r["service"] == TunnelRoutes.mission_control && !r["hostname"].to_s.match?(/\A(admin|hooks)\./) }.map { |r| r["hostname"] }
end
