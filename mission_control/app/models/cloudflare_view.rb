# What Cloudflare has for this Houston, for Settings (docs/plans/cloudflare-settings.md):
# the tunnel and its connections, its live routes against the database's,
# and Houston's DNS records in every zone the token sees. Read-only. Each
# part that fails becomes a problem to show; nothing raises.
class CloudflareView
  Tunnel = Data.define(:id, :name, :status, :created_at)
  Connection = Data.define(:colo, :version, :origin, :since)
  Route = Data.define(:hostname, :path, :service, :drift)
  Record = Data.define(:zone, :name, :project, :proxied, :here)

  TIMEOUT = 5
  PAGE = 100

  attr_reader :tunnel, :connections, :routes, :missing_routes, :records, :problems

  def self.fetch(installation = Installation.current) = new(installation).tap(&:load)

  def initialize(installation)
    @installation = installation
    @tunnel, @connections, @routes, @missing_routes, @records, @problems = nil, [], [], [], [], []
  end

  # A live rule the database doesn't have, or a database rule Cloudflare lacks.
  def drift? = routes.any?(&:drift) || missing_routes.any?

  def load
    @client = Cloudflare::Client.new(@installation.cloudflare_api_token, timeout: TIMEOUT)
    part("tunnel") { load_tunnel }
    part("routes") { load_routes }
    part("records") { load_records }
    self
  end

  def to_h
    { tunnel: tunnel&.to_h, connections: connections.map(&:to_h), routes: routes.map(&:to_h),
      missing_routes: missing_routes.map(&:to_h), drift: drift?, records: records.map(&:to_h), problems: }
  end

  private
    def part(name)
      yield
    rescue Cloudflare::Error => e
      @problems << "#{name}: #{e.message}"
    end

    def tunnel_path = "/accounts/#{@installation.cloudflare_account_id}/cfd_tunnel/#{@installation.tunnel_id}"

    def load_tunnel
      t = @client.get(tunnel_path)
      @tunnel = Tunnel.new(id: t["id"], name: t["name"], status: t["status"], created_at: time(t["created_at"]))
      @connections = Array(t["connections"]).map do |c|
        Connection.new(colo: c["colo_name"].to_s.upcase, version: c["client_version"], origin: c["origin_ip"], since: time(c["opened_at"]))
      end
    end

    def load_routes
      live = Array(@client.get("#{tunnel_path}/configurations").dig("config", "ingress")).map { |r| key(r) }
      expected = TunnelRoutes.rules(@installation.base_domain).map { |r| key(r) }
      @routes = live.map { |k| route(k, drift: !expected.include?(k)) }
      @missing_routes = (expected - live).map { |k| route(k, drift: true) }
    end

    def route((hostname, path, service), drift:) = Route.new(hostname:, path:, service:, drift:)

    def key(rule)
      rule = rule.stringify_keys
      [ rule["hostname"].presence, rule["path"].presence, rule["service"].to_s ]
    end

    def load_records
      here = "#{@installation.tunnel_id}.cfargotunnel.com"
      @records = @client.get("/zones", per_page: 50).flat_map do |zone|
        managed_records(zone["id"]).map do |r|
          Record.new(zone: zone["name"], name: r["name"], project: owner(r), proxied: r["proxied"] == true, here: r["content"] == here)
        end
      end.sort_by { |r| [ r.zone, r.name ] }
    end

    def managed_records(zone_id)
      (1..).each_with_object([]) do |page, all|
        batch = @client.get("/zones/#{zone_id}/dns_records", per_page: PAGE, page:)
        all.concat(batch.select { |r| Cloudflare::Records.managed?(r) })
        break all if batch.size < PAGE
      end
    end

    # "managed-by:houston project:equip" is equip's; a plain mark is one of
    # Houston's own names.
    def owner(record)
      record["comment"].to_s[/project:(\S+)/, 1] || record["name"].to_s[/\A(admin|hooks)\./, 1] || (record["name"].to_s.start_with?("*.") ? "wildcard" : "houston")
    end

    def time(value) = value.present? ? Time.zone.parse(value) : nil
end
