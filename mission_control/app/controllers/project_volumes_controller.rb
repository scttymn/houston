# The project page's Volumes panel: where a volume will live, until Houston
# makes it.
class ProjectVolumesController < ApplicationController
  def update
    project = Project.find_by!(name: params[:project_name])
    location = params[:location].presence && StorageLocation.find_by!(name: params[:location])
    volume = ProjectVolume.choose!(project, params[:name], location)
    redirect_to project_path(project.name), notice: "#{volume.name} will be made on #{volume.where_words} at the next deploy."
  rescue ProjectVolume::Refused, ProjectVolume::Placed => e
    redirect_to project_path(project.name), alert: e.message
  end
end
