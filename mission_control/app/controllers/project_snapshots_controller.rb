# The Snapshots panel's list, a lazy Turbo frame: listing a remote repository
# can take seconds, and the page shouldn't wait for it. Every tab lists, so
# the tabs' counts stay put; the Settings tab (the backup plan) shows even
# when the listing fails.
class ProjectSnapshotsController < ApplicationController
  KINDS = %w[auto deploy settings].freeze

  def index
    @project = Project.find_by!(name: params[:project_name])
    @kind = params[:kind].presence_in(KINDS) || "auto"
    @location = @storage = @project.backup_location
    @last_good_backup = @project.backup_runs.where(status: "go").order(:id).last
    return unless @location

    all = Snapshots.for(@project, @location)
    @counts = all.group_by(&:kind).transform_values(&:size)
    # A deleted project's final snapshot sits with the ones taken before a change.
    @snapshots = all.select { |s| s.kind == @kind || (@kind == "deploy" && s.kind == "final") }
  rescue Snapshots::Unavailable => e
    @error = e.message
  end
end
