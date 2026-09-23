# A project's custom domains (x-houston.domains) in Cloudflare: a proxied
# CNAME to the tunnel for each, commented managed-by:houston project:<name>.
# Houston never changes a record without that exact project's comment, and
# a domain it can't or mustn't point only gets a state; the app still
# deploys at <name>.<base>.
class DomainDns
  Result = Data.define(:state, :reason) do
    def to_h = { "state" => state, "reason" => reason }
  end

  def initialize(project, installation = Installation.current)
    @project = project
    @installation = installation
    @client = Cloudflare::Client.new(installation.cloudflare_api_token)
    @comment = "#{Cloudflare::Records::MANAGED} project:#{project.name}"
  end

  def point(domain)
    if under_base?(domain)
      return Result.new(state: "WILDCARD", reason: nil) unless @installation.dns_mode == "per_host"
      zone = { "id" => @installation.cloudflare_zone_id, "status" => "active" }
    else
      zone = find_zone(domain)
      return Result.new(state: "ZONE NOT IN CLOUDFLARE YET", reason: "Add #{domain.split(".").last(2).join(".")} to Cloudflare (adding a zone is manual), then deploy again") unless zone
    end

    records = Cloudflare::Records.new(@client, zone["id"])
    existing = records.find(domain)
    if existing && existing["comment"] != @comment
      owner = existing["comment"].to_s[/\A#{Cloudflare::Records::MANAGED} project:(\S+)\z/, 1]
      reason = owner ? "#{domain} belongs to project #{owner}" : "#{domain} has a record Houston didn't create for #{@project.name}; remove it in Cloudflare to move the domain here"
      return Result.new(state: "NO-GO", reason:)
    end
    records.point(domain, @installation.tunnel_id, existing:, comment: @comment)
    Result.new(state: zone["status"] == "active" ? "DNS OK" : "DNS PENDING", reason: zone["status"] == "active" ? nil : "the zone's nameservers aren't switched to Cloudflare yet")
  rescue Cloudflare::Error => e
    Result.new(state: "CAN'T CHECK", reason: "Cloudflare: #{e.message}")
  end

  # A domain no longer in compose.yml: its record goes, only if it's this
  # project's. Problems are left for the next sync to find.
  def remove(domain)
    zone = under_base?(domain) ? { "id" => @installation.cloudflare_zone_id } : find_zone(domain)
    return unless zone

    records = Cloudflare::Records.new(@client, zone["id"])
    existing = records.find(domain)
    records.delete(existing) if existing && existing["comment"] == @comment
  rescue Cloudflare::Error => e
    Rails.logger.warn("houston: couldn't remove #{domain}'s record: #{e.message}")
  end

  private
    def under_base?(domain) = domain.end_with?(".#{@installation.base_domain}")

    # The zone holding domain: the domain itself or its nearest parent, never
    # a TLD alone.
    def find_zone(domain)
      labels = domain.split(".")
      (0...(labels.size - 1)).each do |i|
        zone = @client.get("/zones", name: labels[i..].join(".")).first
        return zone if zone
      end
      nil
    end
end
