# First-run setup after the admin exists: where a signed-in admin goes next.
module Setup
  def self.next_step
    Rails.application.routes.url_helpers.setup_cloudflare_path unless Installation.connected?
  end
end
