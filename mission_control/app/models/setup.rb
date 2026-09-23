# First-run setup after the admin exists: where a signed-in admin goes next.
module Setup
  def self.next_step
    routes = Rails.application.routes.url_helpers
    return routes.setup_cloudflare_path unless Installation.connected?
    routes.setup_storage_path unless StorageLocation.default_ready?
  end
end
