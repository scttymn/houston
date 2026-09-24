# The project page's Snapshots panel, a lazy Turbo frame: listing a remote
# repository can take seconds, and the page shouldn't wait for it.
class ProjectSnapshotsController < ApplicationController
  def index
    @project = Project.find_by!(name: params[:project_name])
    @kind = params[:kind] == "deploy" ? "deploy" : "auto"
    @location = @project.backup_location
    return unless @location

    all = Snapshots.for(@project, @location)
    @counts = all.group_by(&:kind).transform_values(&:size)
    @snapshots = all.select { |s| s.kind == @kind }
  rescue Snapshots::Unavailable => e
    @error = e.message
  end
end
