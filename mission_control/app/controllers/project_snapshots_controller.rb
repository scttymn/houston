# The Snapshots panel's list, a lazy Turbo frame: listing a remote repository
# can take seconds, and the page shouldn't wait for it. Its Settings tab is
# the backup plan, which never reads the repository.
class ProjectSnapshotsController < ApplicationController
  KINDS = %w[auto deploy settings].freeze

  def index
    @project = Project.find_by!(name: params[:project_name])
    @kind = params[:kind].presence_in(KINDS) || "auto"
    @location = @storage = @project.backup_location
    @last_good_backup = @project.backup_runs.where(status: "go").order(:id).last
    return if !@location || @kind == "settings"

    all = Snapshots.for(@project, @location)
    @counts = all.group_by(&:kind).transform_values(&:size)
    @snapshots = all.select { |s| s.kind == @kind }
  rescue Snapshots::Unavailable => e
    @error = e.message
  end
end
