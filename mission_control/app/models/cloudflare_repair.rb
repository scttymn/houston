# Repair (docs/plans/cloudflare-settings.md): the tunnel's routes pushed again
# from the database, and Houston's records pointed again: admin. and hooks.
# (host by host), and every deployed project's names and domains. Only
# records Houston made are changed; anything else is reported. Idempotent.
module CloudflareRepair
  Result = Data.define(:item, :state, :reason)

  def self.run(installation = Installation.current)
    results = []
    begin
      TunnelRoutes.push!(installation)
      results << Result.new(item: "routes", state: "OK", reason: nil)
    rescue Cloudflare::Error => e
      results << Result.new(item: "routes", state: "NO-GO", reason: e.message)
    end
    per_host = installation.dns_mode == "per_host"
    results.concat(houston_names(installation)) if per_host

    Project.order(:name).each do |project|
      next unless project.running_deploy # a project's names are pointed at its first GO
      dns = DomainDns.new(project, installation)
      host = project.host(installation)
      results << result(host, dns.point(host)) if per_host
      states = project.domains.reject { |d| d == host }.index_with { |domain| dns.point(domain).to_h }
      project.update!(domain_states: states)
      states.each { |domain, s| results << Result.new(item: domain, state: s["state"], reason: s["reason"]) }
    end
    results
  end

  def self.houston_names(installation)
    client = Cloudflare::Client.new(installation.cloudflare_api_token)
    records = Cloudflare::Records.new(client, installation.cloudflare_zone_id)
    %w[admin hooks].map do |label|
      name = "#{label}.#{installation.base_domain}"
      existing = records.find(name)
      if existing && !Cloudflare::Records.managed?(existing)
        Result.new(item: name, state: "NO-GO", reason: "#{name} has a record Houston didn't create; remove it in Cloudflare, then repair again")
      else
        records.point(name, installation.tunnel_id, existing:)
        Result.new(item: name, state: "DNS OK", reason: nil)
      end
    rescue Cloudflare::Error => e
      Result.new(item: name, state: "CAN'T CHECK", reason: "Cloudflare: #{e.message}")
    end
  end
  private_class_method :houston_names

  def self.result(item, dns) = Result.new(item:, state: dns.state, reason: dns.reason)
  private_class_method :result
end
