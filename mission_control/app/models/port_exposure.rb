# Whether Mission Control's port 3000 is open to the network. The installer's
# first run publishes it on every interface, for setup in a browser; once
# admin.<base> is the way in, a rerun binds it to 127.0.0.1 (security audit
# H3). Read from this container's own port bindings, at most hourly per
# container: they can't change without recreating it, and an unknown answer
# (docker failing, say) is tried again. Unknown is closed: nothing to say.
module PortExposure
  EVERYWHERE = [ "", "0.0.0.0", "::" ].freeze

  def self.open?
    Rails.cache.fetch([ "port-exposure", Socket.gethostname ], expires_in: 1.hour, skip_nil: true) { read } || false
  end

  # true or false; nil when docker can't say (not cached).
  def self.read
    result = DockerCommand.run("inspect", "--format", "{{json .HostConfig.PortBindings}}", Socket.gethostname, timeout: 5)
    return nil unless result.success
    bindings = JSON.parse(result.output)
    bindings.is_a?(Hash) && bindings.values.flatten.compact.any? { |binding| binding["HostIp"].to_s.in?(EVERYWHERE) }
  rescue JSON::ParserError
    nil
  end
  private_class_method :read
end
