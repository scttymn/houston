# The tunnel's ingress, built from the database: the one place that knows the
# rules. Mission Control answers admin.<base>, hooks.<base>'s webhook paths,
# and the hostnames of projects in maintenance (Houston's maintenance page);
# everything else goes to kamal-proxy.
class TunnelRoutes
  def self.mission_control = ENV.fetch("HOUSTON_MISSION_CONTROL_URL", "http://mission-control:80")

  def self.rules(base_domain)
    installation = Installation.new(base_domain:)
    maintenance = Project.where.not(maintenance_since: nil).order(:name).flat_map { |p| p.hostnames(installation) }.uniq
    [
      { hostname: "admin.#{base_domain}", service: mission_control },
      { hostname: "hooks.#{base_domain}", path: "^/[a-z0-9-]+$", service: mission_control },
      { hostname: "hooks.#{base_domain}", service: "http_status:404" },
      *maintenance.map { |hostname| { hostname:, service: mission_control } },
      { service: ENV.fetch("HOUSTON_APPS_URL", "http://kamal-proxy:80") }
    ]
  end

  # Replaces the tunnel's configuration with the rules as the database has them now.
  def self.push!(installation = Installation.current)
    Cloudflare::Client.new(installation.cloudflare_api_token)
      .put("/accounts/#{installation.cloudflare_account_id}/cfd_tunnel/#{installation.tunnel_id}/configurations",
           { config: { ingress: rules(installation.base_domain) } })
  end
end
