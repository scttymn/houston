# Delete a project (docs/plans/delete-project.md): the confirm page, in the
# Restore dialog's style, and asking. Asking again for a deletion that
# stopped partway (Finish deleting) resumes it.
class ProjectDeletionsController < ApplicationController
  before_action :set_project

  def new; end

  def create
    deletion = ProjectDeletion.request!(@project, confirm: params[:confirm].to_s, delete_backups: params[:delete_backups] == "1",
                                                  by: Current.session.user.email_address)
    redirect_to deletion_path(deletion)
  rescue ProjectDeletion::Refused => e
    @error = e.message
    render :new, status: :unprocessable_entity
  end

  private
    def set_project
      @project = Project.find_by!(name: params[:project_name])
      @installation = Installation.current
      @storage = @project.backup_location
      @kept = @project.running_deploy.present? && (@project.volumes.any? || @project.databases.any?)
      @backup_locations = Snapshots.locations_for(@project)
    end
end
