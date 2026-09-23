# Requests for one of a project's hostnames. They reach Mission Control only
# while the tunnel routes them here (the project's maintenance page); for
# these hosts it serves that page and nothing else.
class AppHost
  def self.matches?(request) = !project_for(request.host).nil?

  def self.project_for(host)
    installation = Installation.current
    return nil if installation.base_domain.blank? || host.in?([ "admin.#{installation.base_domain}", "hooks.#{installation.base_domain}" ])

    Project.find_each.find { |p| p.hostnames(installation).include?(host) }
  end
end
