# The Backup plan panel's STORAGE: where this project's backups go.
class ProjectBackupTargetsController < ApplicationController
  def update
    project = Project.find_by!(name: params[:project_name])
    location = params[:location].presence && StorageLocation.find_by!(name: params[:location])
    project.choose_backup_location!(location)
    redirect_to project_path(project.name), notice: "#{project.name} backs up to #{project.backup_location&.name}#{" (the default)" unless location}."
  rescue Project::Refused => e
    redirect_to project_path(project.name), alert: e.message
  end
end
