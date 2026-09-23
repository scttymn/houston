require "net/http"

# Live status for the header strip and the pre-flight check. Each probe is
# cached for 30 seconds and times out after 2, so pages stay fast and
# Cloudflare isn't asked on every page view.
module SystemStatus
  Tunnel = Data.define(:state, :connections, :reason) do
    def go? = state == :go
  end

  # Whether admin.<base> reaches this Mission Control through Cloudflare.
  Route = Data.define(:state, :reason)

  CACHE_FOR = 30.seconds
  TIMEOUT = 2
  # How long Cloudflare gets to move admin.<base> onto the tunnel after
  # step 2 before a miss is NO-GO. Its edge can keep sending a name to an
  # older record (another server's wildcard) for a while.
  SWITCH_OVER = 10.minutes
  RECHECK_MISS_AFTER = 5.seconds

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

  # The probe goes out through Cloudflare's edge and back in through the
  # tunnel; only this install's /ping answers with Installation.identity.
  # The probe's result is cached, not the state: HOLD turns into NO-GO with
  # the clock.
  def self.admin_route(installation = Installation.current, on_admin: false)
    return unless installation.connected? && installation.base_domain.present?
    return Route.new(state: :go, reason: nil) if on_admin

    miss = Rails.cache.read([ "system_status/admin_route", installation.base_domain ]) || begin
      miss = probe_admin(installation.base_domain)
      Rails.cache.write([ "system_status/admin_route", installation.base_domain ], miss, expires_in: miss.empty? ? CACHE_FOR : RECHECK_MISS_AFTER)
      miss
    end

    if miss.empty? then Route.new(state: :go, reason: nil)
    elsif installation.cloudflare_connected_at > SWITCH_OVER.ago then Route.new(state: :hold, reason: miss)
    else Route.new(state: :nogo, reason: miss)
    end
  end

  # "" when admin.<base> answered as this install, otherwise what happened.
  def self.probe_admin(base_domain)
    host = "admin.#{base_domain}"
    response = Net::HTTP.start(host, 443, use_ssl: true, open_timeout: TIMEOUT, read_timeout: TIMEOUT) { |http| http.get("/ping") }
    code = response.code.to_i
    if code == 200 && ActiveSupport::SecurityUtils.secure_compare(response.body.to_s.strip, Installation.identity) then ""
    elsif code == 530 then "530 from Cloudflare: its edge is still sending #{host} to a tunnel that isn't this one"
    else "answered #{code}, not from this Mission Control"
    end
  rescue Net::OpenTimeout, Net::ReadTimeout
    "no answer in #{TIMEOUT} s"
  rescue SocketError
    "can't look up #{host} from this server"
  rescue SystemCallError, OpenSSL::SSL::SSLError => e
    "couldn't connect to #{host} from this server (#{e.message})"
  end
  private_class_method :probe_admin

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
