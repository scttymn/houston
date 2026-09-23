# First-run step 2: check the API token (reads only), then create the tunnel,
# its ingress rules and the *.<base> wildcard. Every create step reuses what
# already exists, so a rerun finishes whatever an earlier attempt didn't.
class CloudflareSetup
  include ActiveModel::Model
  include ActiveModel::Attributes

  attribute :base_domain, :string
  attribute :api_token, :string

  Check = Data.define(:ok, :label)
  class Stop < StandardError; end

  LABEL = /\A[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\z/
  MANAGED = "managed-by:houston"

  validates :api_token, presence: true
  validate :base_domain_is_a_domain

  attr_reader :checks

  def initialize(...)
    super
    @checks = []
  end

  def base_domain=(value)
    super(value.to_s.strip.downcase)
  end

  def tunnel_name
    "houston-#{base_domain.to_s.split(".").first.presence || "<base>"}"
  end

  def save
    return false unless valid?

    @client = Cloudflare::Client.new(api_token)
    account = check_account
    check_tunnel_access(account)
    zone = check_zone
    raise Stop unless checks.all?(&:ok)

    tunnel_id = find_or_create_tunnel(account)
    tunnel_token = step("getting the tunnel's token") { @client.get("/accounts/#{account}/cfd_tunnel/#{tunnel_id}/token") }
    step("setting the tunnel's routes") { @client.put("/accounts/#{account}/cfd_tunnel/#{tunnel_id}/configurations", { config: { ingress: } }) }
    dns_mode = point_dns(zone, tunnel_id)

    installation = Installation.current
    installation.update!(base_domain:, cloudflare_account_id: account, cloudflare_zone_id: zone, tunnel_id:,
                         cloudflare_api_token: api_token, tunnel_token:, dns_mode:, cloudflare_connected_at: Time.current)
    hand_token_to_cloudflared(tunnel_token)
    true
  rescue Stop
    false
  end

  private
    def base_domain_is_a_domain
      labels = base_domain.to_s.split(".", -1)
      unless labels.size >= 2 && base_domain.length <= 253 && labels.all? { |l| l.match?(LABEL) }
        errors.add(:base_domain, "must be a domain like example.com, with no scheme, path or wildcard")
      end
    end

    def check_account
      accounts = @client.get("/accounts", per_page: 50)
      case accounts.size
      when 0 then fail!("The token can't see any Cloudflare account. Give it access to the account that holds #{base_domain}.")
      when 1 then pass!("Account · #{accounts.first["name"]}") && accounts.first["id"]
      else fail!("The token can see more than one account; make one for just the account that holds #{base_domain}.")
      end
    rescue Cloudflare::Error => e
      fail!(e.status.in?([ 400, 401, 403 ]) ? "The token isn't valid (Cloudflare: #{e.message})." : "Cloudflare: #{e.message}")
    end

    def check_tunnel_access(account)
      return unless account
      @client.get("/accounts/#{account}/cfd_tunnel", per_page: 1)
      pass!("Account · Cloudflare Tunnel")
    rescue Cloudflare::Error => e
      fail!("Account · Cloudflare Tunnel: the token needs Cloudflare Tunnel · Edit on the account (Cloudflare: #{e.message}).")
    end

    def check_zone
      zone = @client.get("/zones", name: base_domain).first
      return fail!("#{base_domain} isn't one of the token's zones. Give it Zone · DNS · Edit on #{base_domain} (the zone must be in this Cloudflare account).") unless zone
      @client.get("/zones/#{zone["id"]}/dns_records", per_page: 1)
      pass!("Zone · DNS on #{base_domain}") && zone["id"]
    rescue Cloudflare::Error => e
      fail!("Zone · DNS on #{base_domain}: the token needs Zone · DNS · Edit (Cloudflare: #{e.message}).")
    end

    def find_or_create_tunnel(account)
      step("creating the tunnel", denied: "The token can read tunnels but not create them: give it Cloudflare Tunnel · Edit on the account (not only Read), then try again.") do
        existing = @client.get("/accounts/#{account}/cfd_tunnel", name: tunnel_name, is_deleted: "false").first
        existing ? existing["id"] : @client.post("/accounts/#{account}/cfd_tunnel", { name: tunnel_name, config_src: "cloudflare" })["id"]
      end
    end

    # Owns *.<base> when it's free (or already Houston's). When another server
    # owns it (a migration in progress), Houston never touches it and adds
    # explicit records instead: admin. and hooks. now, each app's name when it
    # is first deployed. Explicit records win over the wildcard. Every name is
    # checked before anything is written.
    def point_dns(zone, tunnel_id)
      wildcard_name = "*.#{base_domain}"
      wildcard = step("looking up #{wildcard_name}") { find_record(zone, wildcard_name) }
      if wildcard.nil? || managed?(wildcard)
        upsert(zone, tunnel_id, wildcard_name, wildcard)
        return "wildcard"
      end

      names = %w[admin hooks].map { |label| "#{label}.#{base_domain}" }
      existing = names.to_h { |name| [ name, step("looking up #{name}") { find_record(zone, name) } ] }
      foreign = existing.select { |_, record| record && !managed?(record) }.keys
      if foreign.any?
        foreign.each do |name|
          fail!("#{name} already exists and Houston didn't create it (no #{MANAGED} comment). Another server owns *.#{base_domain}, so Houston needs its own #{name}; remove that record in Cloudflare, then try again.")
        end
        raise Stop
      end
      existing.each { |name, record| upsert(zone, tunnel_id, name, record) }
      "per_host"
    end

    def find_record(zone, name)
      @client.get("/zones/#{zone}/dns_records", name:).first
    end

    def managed?(record)
      record["comment"].to_s.start_with?(MANAGED)
    end

    def upsert(zone, tunnel_id, name, existing)
      record = { type: "CNAME", name:, content: "#{tunnel_id}.cfargotunnel.com", proxied: true, comment: MANAGED }
      step("pointing #{name} at the tunnel", denied: "The token can read DNS but not change it: give it Zone · DNS · Edit on #{base_domain} (not only Read), then try again.") do
        existing ? @client.patch("/zones/#{zone}/dns_records/#{existing["id"]}", record) : @client.post("/zones/#{zone}/dns_records", record)
      end
    end

    # On the server, cloudflared runs `tunnel run --token-file` on a volume it
    # shares with Mission Control; writing the file is all it takes to connect.
    def hand_token_to_cloudflared(token)
      path = ENV["HOUSTON_TUNNEL_TOKEN_PATH"].presence or return
      tmp = "#{path}.tmp"
      File.open(tmp, "w", 0o600) { |f| f.write(token) }
      File.chmod(0o600, tmp)
      File.rename(tmp, path)
    end

    def ingress
      mission_control = ENV.fetch("HOUSTON_MISSION_CONTROL_URL", "http://mission-control:80")
      [
        { hostname: "admin.#{base_domain}", service: mission_control },
        { hostname: "hooks.#{base_domain}", path: "^/[a-z0-9-]+$", service: mission_control },
        { hostname: "hooks.#{base_domain}", service: "http_status:404" },
        { service: ENV.fetch("HOUSTON_APPS_URL", "http://kamal-proxy:80") }
      ]
    end

    # denied: what to tell the admin when Cloudflare refuses for lack of
    # permission (the read-only token check can't prove edit rights).
    def step(doing, denied: nil)
      yield
    rescue Cloudflare::Error => e
      fail!(denied && e.status.in?([ 401, 403 ]) ? "#{denied} (Cloudflare: #{e.message})" : "Cloudflare said no while #{doing}: #{e.message}.")
      raise Stop
    end

    def pass!(label)
      @checks << Check.new(ok: true, label:)
    end

    def fail!(label)
      @checks << Check.new(ok: false, label:)
      nil
    end
end
