# POST /api/projects/sync: houston deploy sends what it read from compose.yml.
# Validates the payload the way the CLI does (Mission Control doesn't trust
# its callers' parsing), saves the project and the container names it owns,
# then points DNS. See docs/plans/deploy-path.md, Batch 2.
class ProjectSync
  class Refused < StandardError; end

  NAME = /\A[a-z]([a-z0-9-]{0,61}[a-z0-9])?\z/
  RESERVED = %w[admin hooks].freeze
  SERVICE = /\A[a-zA-Z0-9][a-zA-Z0-9_.-]*\z/
  VARIABLE = /\A[A-Za-z_][A-Za-z0-9_]*\z/
  LABEL = /\A[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\z/

  attr_reader :errors, :project

  def initialize(payload, installation: Installation.current)
    @payload = payload.is_a?(Hash) ? payload : {}
    @installation = installation
    @errors = Hash.new { |h, k| h[k] = [] }
  end

  def valid?
    errors.clear
    validate
    errors.empty?
  end

  # Saves the project and its host names (and link) in one transaction.
  # Raises Refused when another project owns one of the names.
  # link: the repo link's attributes, when Add project saves it.
  def save!(link: nil)
    Project.transaction do
      @project = Project.find_or_initialize_by(name: @payload["name"])
      @dropped_domains = @project.domains.to_a - @payload["domains"].to_a
      @project.update!(app_service: @payload["app_service"], services: @payload["services"], domains: @payload["domains"],
                       variables: @payload["variables"].map { |v| { "name" => v["name"], "required" => v["required"] == true } },
                       health: @payload["health"], port: @payload["port"], deploy_rule: @payload["deploy_rule"] || {},
                       synced_at: Time.current, **link.to_h)
      claim_hosts
    end
    @project
  rescue ActiveRecord::RecordNotUnique
    raise Refused, clash_message || "another project claimed one of #{@payload["name"]}'s container names; try again"
  end

  # Host-by-host: <name>.<base> needs its own record. Returns the DNS mode.
  def point_dns!
    return "wildcard" unless @installation.dns_mode == "per_host"

    records = Cloudflare::Records.new(Cloudflare::Client.new(@installation.cloudflare_api_token), @installation.cloudflare_zone_id)
    name = project.host(@installation)
    existing = records.find(name)
    if existing && !Cloudflare::Records.managed?(existing)
      raise Refused, "NO-GO: #{name} already exists and Houston didn't create it (no #{Cloudflare::Records::MANAGED} comment). Remove it in Cloudflare, then deploy again."
    end
    records.point(name, @installation.tunnel_id, existing:, comment: "#{Cloudflare::Records::MANAGED} project:#{project.name}")
    "per_host"
  end

  # Custom domains: points each, removes the records of dropped ones, and
  # stores the states. Returns domain → { state, reason }.
  def point_domains!
    dns = DomainDns.new(project, @installation)
    @dropped_domains.to_a.each { |domain| dns.remove(domain) }
    states = project.domains.reject { |d| d == project.host(@installation) }.index_with { |domain| dns.point(domain).to_h }
    project.update!(domain_states: states)
    states
  end

  private
    # The unique index on project_hosts.name decides: a name another project
    # owns raises RecordNotUnique, which rolls the whole sync back.
    def claim_hosts
      names = @project.host_names
      @project.hosts.where.not(name: names).delete_all
      (names - @project.hosts.pluck(:name)).each { |name| @project.hosts.create!(name:) }
    end

    def clash_message
      names = Project.new(name: @payload["name"], services: @payload["services"], app_service: @payload["app_service"]).host_names
      taken = ProjectHost.includes(:project).where(name: names).where.not(project: { name: @payload["name"] }).first
      taken && "the container name #{taken.name} belongs to project #{taken.project.name}; rename a service or the project"
    end

    def validate
      name = @payload["name"]
      errors["name"] << "must be a DNS label (a-z, 0-9, -)" unless name.is_a?(String) && name.match?(NAME)
      errors["name"] << "#{name} is reserved" if RESERVED.include?(name)

      services = @payload["services"]
      if !services.is_a?(Array) || services.empty? || !services.all? { |s| s.is_a?(String) && s.match?(SERVICE) }
        errors["services"] << "must be a list of compose service names"
      elsif !services.include?(@payload["app_service"])
        errors["app_service"] << "must be one of the services"
      end

      variables = @payload["variables"]
      unless variables.is_a?(Array) && variables.all? { |v| v.is_a?(Hash) && v["name"].is_a?(String) && v["name"].match?(VARIABLE) }
        errors["variables"] << "must be a list of {name, required} with variable names"
      end

      domains = @payload["domains"]
      unless domains.is_a?(Array) && domains.all? { |d| domain?(d) }
        errors["domains"] << "must be a list of lowercase hostnames"
      end

      port = @payload["port"]
      errors["port"] << "must be a port number" unless port.is_a?(Integer) && port.between?(1, 65_535)

      health = @payload["health"]
      errors["health"] << "must be a path starting with /" unless health.is_a?(String) && health.match?(%r{\A/\S*\z})

      rule = @payload["deploy_rule"]
      errors["deploy_rule"] << "must be an object" unless rule.nil? || rule.is_a?(Hash)
    end

    def domain?(d)
      return false unless d.is_a?(String) && d.length <= 253
      labels = d.split(".", -1)
      labels.size >= 2 && labels.all? { |l| l.match?(LABEL) }
    end
end
