# Create Snapshot, from the project page.
class ProjectBackupsController < ApplicationController
  def create
    project = Project.find_by!(name: params[:project_name])
    BackupRun.request!(project)
    redirect_to project_path(project.name), notice: "Backing up #{project.name}."
  rescue BackupRun::Refused => e
    redirect_to project_path(project.name), alert: "Can't back up #{project.name}: #{e.message}."
  end
end
