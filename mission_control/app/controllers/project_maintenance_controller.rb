# The project page's maintenance switch.
class ProjectMaintenanceController < ApplicationController
  # The page as the project's hostnames would show it. A repo's page may
  # carry scripts: the sandbox CSP keeps them off Mission Control's origin,
  # even when this is opened directly rather than in the page's iframe.
  def preview
    @project = Project.find_by!(name: params[:project_name])
    @project.maintenance_message = @project.maintenance_message.presence || "(your message)"
    response.headers["Content-Security-Policy"] = "sandbox"
    response.headers["Cache-Control"] = "no-store"
    if (html = MaintenancePage.html(@project))
      render html: html.html_safe
    else
      render "maintenance_pages/show", layout: false
    end
  end

  def update
    project = Project.find_by!(name: params[:project_name])
    if params[:on] == "1"
      Maintenance.on!(project, by: Current.session.user.email_address, message: params[:message])
      redirect_to project_path(project.name), notice: "#{project.name} shows the maintenance page. It stays up until you turn it off."
    else
      Maintenance.off!(project)
      redirect_to project_path(project.name), notice: "#{project.name} is back: its hostnames go to the app again."
    end
  rescue Maintenance::Failed => e
    redirect_to project_path(project.name), alert: e.message
  rescue ActiveRecord::RecordInvalid => e
    redirect_to project_path(project.name), alert: e.record.errors.full_messages.to_sentence
  end
end
