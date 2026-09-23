# Requests for hooks.<base>. The tunnel sends any one-segment path there to
# Mission Control; the routes only answer the webhook and the ping on it.
class HooksHost
  def self.matches?(request)
    base = Installation.current.base_domain
    base.present? && request.host == "hooks.#{base}"
  end
end
