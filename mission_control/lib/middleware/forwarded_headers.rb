require "ipaddr"
require "resolv"

# Makes what Rails reads about a request's client true, whichever way it
# came (docs/plans/security-fixes.md, M1 and L1):
#
# - Thruster, in front of Puma in the same container, appends the address
#   that connected to it to X-Forwarded-For, and passes on whatever else the
#   client sent. So only X-Forwarded-For's last entry is known, and only
#   when the request came through Thruster (from loopback).
# - cloudflared connects from its own container. For its requests
#   Cloudflare's Cf-Connecting-Ip is the visitor. Every request through the
#   tunnel carries Cf-Ray (a client can't remove it), and Cloudflare sets
#   X-Forwarded-Proto to the visitor's scheme.
#
# So REMOTE_ADDR becomes the client, and the client's own forwarding headers
# are dropped: request.remote_ip is the client (rate limits key on it),
# request.host the Host header (hooks.<base> and app names match on it), and
# request.ssl? true only for https through the tunnel. (A client faking
# Cf-Ray can claim https for itself, which gains it nothing.)
class ForwardedHeaders
  FORWARDING = %w[ HTTP_X_FORWARDED_FOR HTTP_CLIENT_IP HTTP_X_FORWARDED_HOST HTTP_X_FORWARDED_PORT HTTP_X_FORWARDED_SERVER HTTP_FORWARDED ].freeze
  SCHEME = %w[ HTTP_X_FORWARDED_PROTO HTTP_X_FORWARDED_SCHEME HTTP_X_FORWARDED_SSL ].freeze
  LOOKUP_EVERY = 60 # seconds

  # cloudflared's addresses, looked up in Docker's DNS at most once a minute,
  # and only for a request carrying Cf-Connecting-Ip. A failed lookup trusts
  # no one: the tunnel's visitors then share cloudflared's address.
  class_attribute :tunnel_addresses, default: -> { ForwardedHeaders.cloudflared }

  def self.cloudflared
    now = Process.clock_gettime(Process::CLOCK_MONOTONIC)
    @cloudflared = nil if @cloudflared && now - @cloudflared[:at] > LOOKUP_EVERY
    (@cloudflared ||= { at: now, addresses: lookup })[:addresses]
  end

  def self.lookup
    dns = Resolv::DNS.new.tap { |d| d.timeouts = 1 }
    Resolv.new([ Resolv::Hosts.new, dns ]).getaddresses(ENV.fetch("HOUSTON_TUNNEL_HOST", "cloudflared"))
  rescue Resolv::ResolvError, SystemCallError
    []
  end

  def initialize(app)
    @app = app
  end

  def call(env)
    peer = env["REMOTE_ADDR"]
    thruster = env["HTTP_X_FORWARDED_FOR"].to_s.split(",").last.to_s.strip
    peer = thruster if loopback?(peer) && thruster.present?

    visitor = env["HTTP_CF_CONNECTING_IP"].to_s.strip
    tunnel = visitor.present? && self.class.tunnel_addresses.call.include?(peer)
    env["REMOTE_ADDR"] = tunnel ? visitor : peer
    FORWARDING.each { |name| env.delete(name) }
    if env["HTTP_CF_RAY"].blank?
      SCHEME.each { |name| env.delete(name) }
      # Puma already set these from the client's X-Forwarded-Proto.
      env["rack.url_scheme"] = "http"
      env.delete("HTTPS")
    end
    @app.call(env)
  end

  private
    def loopback?(address)
      IPAddr.new(address.to_s).loopback?
    rescue IPAddr::InvalidAddressError
      false
    end
end
