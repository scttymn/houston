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
end
