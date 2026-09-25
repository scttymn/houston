# What Mission Control's port 3000 is bound to, read from this container's
# own port bindings, at most hourly per container: they can't change
# without recreating it, and an unknown answer (docker failing, say) isn't
# kept, so it's asked again. nil when unknown.
module PortExposure
  EVERYWHERE = [ "", "0.0.0.0", "::" ].freeze

  # "0.0.0.0" (every interface), "127.0.0.1", another address, or nil.
  def self.address
    Rails.cache.fetch([ "port-exposure", Socket.gethostname ], expires_in: 1.hour, skip_nil: true) { read }
  end

  def self.open? = address.present? && address != "127.0.0.1"

  def self.read
    result = DockerCommand.run("inspect", "--format", "{{json .HostConfig.PortBindings}}", Socket.gethostname, timeout: 5)
    return nil unless result.success
    bindings = JSON.parse(result.output)
    ip = bindings.is_a?(Hash) ? bindings.values.flatten.compact.first&.dig("HostIp") : nil
    ip && (EVERYWHERE.include?(ip) ? "0.0.0.0" : ip)
  rescue JSON::ParserError
    nil
  end
  private_class_method :read
end
