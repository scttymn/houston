# Houston's maintenance page, for a project's hostnames while it's in
# maintenance; a plain 404 for them otherwise (a stale route). Nothing else
# of Mission Control answers on these hosts: no session, no chrome.
class MaintenancePagesController < ActionController::Base
  # Any method on any path gets the page; there's no session or state here to protect.
  skip_forgery_protection

  def show
    @project = AppHost.project_for(request.host)
    return head :not_found unless @project&.maintenance?

    response.headers["Retry-After"] = "60"
    response.headers["Cache-Control"] = "no-store"
    if (html = MaintenancePage.html(@project))
      render html: html.html_safe, status: :service_unavailable
    else
      render :show, layout: false, status: :service_unavailable
    end
  end
end
