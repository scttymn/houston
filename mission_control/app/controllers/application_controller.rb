class ApplicationController < ActionController::Base
  include Authentication
  # Only allow modern browsers supporting webp images, web push, badges, import maps, CSS nesting, and CSS :has.
  allow_browser versions: :modern

  # Changes to the importmap will invalidate the etag for HTML responses
  stale_when_importmap_changes

  # Until the admin exists, every page leads to first-run setup; after that,
  # a signed-in admin is taken to the next unfinished setup step.
  prepend_before_action :require_setup

  private
    def require_setup
      return redirect_to(setup_path) unless User.exists?
      return if controller_name == "sessions"
      step = Setup.next_step
      redirect_to step if step && authenticated?
    end
end
