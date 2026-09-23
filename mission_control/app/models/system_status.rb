require "net/http"

# Live status for the header strip and the pre-flight check. Each probe is
# cached for 30 seconds and times out after 2, so pages stay fast and
# Cloudflare isn't asked on every page view.
module SystemStatus
  Tunnel = Data.define(:state, :connections, :reason) do
    def go? = state == :go
  end

  CACHE_FOR = 30.seconds
  TIMEOUT = 2

  def self.tunnel(installation = Installation.current)
    return Tunnel.new(state: :unknown, connections: 0, reason: "not connected to Cloudflare") unless installation.tunnel_id && installation.cloudflare_api_token

    Rails.cache.fetch([ "system_status/tunnel", installation.tunnel_id ], expires_in: CACHE_FOR) do
      client = Cloudflare::Client.new(installation.cloudflare_api_token, timeout: TIMEOUT)
      connections = Array(client.get("/accounts/#{installation.cloudflare_account_id}/cfd_tunnel/#{installation.tunnel_id}")["connections"]).size
      Tunnel.new(state: connections.positive? ? :go : :hold, connections:, reason: nil)
    rescue Cloudflare::Error => e
      Tunnel.new(state: :unknown, connections: 0, reason: e.message)
    end
  end

  def self.registry_up?
    Rails.cache.fetch("system_status/registry", expires_in: CACHE_FOR) do
      uri = URI("#{ENV.fetch("HOUSTON_REGISTRY_URL", "http://registry:5000")}/v2/")
      response = Net::HTTP.start(uri.host, uri.port, use_ssl: uri.scheme == "https", open_timeout: TIMEOUT, read_timeout: TIMEOUT) { |http| http.get(uri.path) }
      response.code.to_i < 500 # 401 still means the registry is up
    rescue Net::OpenTimeout, Net::ReadTimeout, SocketError, SystemCallError, OpenSSL::SSL::SSLError
      false
    end
  end
end
