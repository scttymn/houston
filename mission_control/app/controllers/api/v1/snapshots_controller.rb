class Api::V1::SnapshotsController < Api::V1::BaseController
  def index
    project = Project.find_by(name: params[:project_name])
    return render json: { error: "no project #{params[:project_name]}" }, status: :not_found unless project
    location = project.backup_location
    return render json: { error: "no backup storage yet (finish setup's storage step)" }, status: :conflict unless location

    render json: { snapshots: Snapshots.for(project, location).map { |s| RemoteView.snapshot(s) } }
  rescue Snapshots::Unavailable => e
    render json: { error: "can't read snapshots: #{e.message}" }, status: :bad_gateway
  end

  # Everything in one snapshot as a zip, streamed from restic (houston
  # snapshots download; docs/plans/download-snapshot.md). location: the
  # project's backup location by default. Every refusal comes before a byte.
  def download
    project = Project.find_by(name: params[:project_name])
    return render json: { error: "no project #{params[:project_name]}" }, status: :not_found unless project
    location = params[:location].presence || project.backup_location&.name
    return render json: { error: "no backup storage yet (finish setup's storage step)" }, status: :conflict unless location

    export = SnapshotExport.open(project, location_name: location, snapshot: params[:id].to_s, by: "token #{@api_token.name}")
    response.headers.merge!(export.headers)
    self.response_body = export
  rescue SnapshotExport::NotFound => e
    render json: { error: e.message }, status: :not_found
  rescue SnapshotExport::Busy => e
    render json: { error: e.message }, status: :conflict
  rescue SnapshotExport::Failed => e
    render json: { error: e.message }, status: :bad_gateway
  end
end
