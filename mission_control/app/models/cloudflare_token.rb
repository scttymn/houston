# Replacing Houston's Cloudflare API token (docs/plans/cloudflare-settings.md).
# Every check runs with the new token before it's saved: the same account,
# this server's tunnel, DNS on the base zone and on every project domain's
# zone. Any failure keeps the old token. The checks are reads, so they can't
# prove edit rights; their labels say which permissions to grant.
class CloudflareToken
  Check = Data.define(:ok, :label)

  attr_reader :checks

  def initialize(token, installation = Installation.current)
    @token = token.to_s.strip
    @installation = installation
    @checks = []
  end

  def replace
    return fail!("Paste the new token's value") && false if @token.empty?

    @client = Cloudflare::Client.new(@token)
    return false unless check_account
    check_tunnel
    zones_to_check.each { |name| check_zone(name) }
    return false unless checks.all?(&:ok)

    @installation.update!(cloudflare_api_token: @token)
    Rails.cache.delete([ "system_status/tunnel", @installation.tunnel_id ])
    true
  end

  private
    def pass!(label) = @checks << Check.new(ok: true, label:)

    def fail!(label)
      @checks << Check.new(ok: false, label:)
      nil
    end

    def check_account
      account = @client.get("/accounts", per_page: 50).find { |a| a["id"] == @installation.cloudflare_account_id }
      return fail!("Account: the token can't see the Cloudflare account Houston uses. Make it for that account.") unless account
      pass!("Account · #{account["name"]}")
    rescue Cloudflare::Error => e
      fail!(e.status.in?([ 400, 401, 403 ]) ? "The token isn't valid (Cloudflare: #{e.message}). Copy the token's value (not its ID), and check it's active." : "Cloudflare: #{e.message}")
    end

    def check_tunnel
      @client.get("/accounts/#{@installation.cloudflare_account_id}/cfd_tunnel/#{@installation.tunnel_id}")
      pass!("Cloudflare Tunnel · this server's tunnel")
    rescue Cloudflare::Error => e
      fail!("Cloudflare Tunnel: the token needs Account · Cloudflare Tunnel · Edit (Cloudflare: #{e.message}).")
    end

    # The base domain, then the zone of each project domain outside it.
    def zones_to_check
      base = @installation.base_domain
      outside = Project.pluck(:domains).flatten.compact.uniq.reject { |d| d == base || d.end_with?(".#{base}") }
      [ base, *outside ]
    end

    def check_zone(domain)
      zone = find_zone(domain)
      return if zone && @checked&.include?(zone["id"])
      return fail!("Zone · DNS for #{domain}: the token can't see its zone. Give it Zone · DNS · Edit on it (the zone must be in this account).") unless zone
      (@checked ||= []) << zone["id"]
      @client.get("/zones/#{zone["id"]}/dns_records", per_page: 1)
      pass!("Zone · DNS on #{zone["name"]}")
    rescue Cloudflare::Error => e
      fail!("Zone · DNS on #{zone ? zone["name"] : domain}: the token needs Zone · DNS · Edit (Cloudflare: #{e.message}).")
    end

    # The domain's zone: itself or its nearest parent, never a TLD alone.
    def find_zone(domain)
      labels = domain.split(".")
      (0...(labels.size - 1)).each do |i|
        zone = @client.get("/zones", name: labels[i..].join(".")).first
        return zone if zone
      end
      nil
    end
end
