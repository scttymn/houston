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

  # Everything in one snapshot as a zip, streamed from restic
  # (docs/plans/download-snapshot.md). A refusal comes before any byte, so
  # it can still be a redirect; a failure after them breaks the connection,
  # and the browser marks the download failed.
  def download
    project = Project.find_by!(name: params[:project_name])
    export = SnapshotExport.open(project, location_name: params[:location].to_s, snapshot: params[:id].to_s, by: Current.user.email_address)
    response.headers.merge!(export.headers)
    self.response_body = export
  rescue SnapshotExport::NotFound => e
    raise ActiveRecord::RecordNotFound, e.message
  rescue SnapshotExport::Busy, SnapshotExport::Failed => e
    redirect_to project_path(project.name), alert: "Can't download snapshot #{params[:id].to_s.truncate(64)}: #{e.message}"
  end
end
