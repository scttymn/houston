class Api::V1::RestoresController < Api::V1::BaseController
  # {snapshot, location? (default: the project's backup location), confirm}
  def create
    project = Project.find_by(name: params[:project_name])
    return render json: { error: "no project #{params[:project_name]}" }, status: :not_found unless project

    location = params[:location].present? ? StorageLocation.find_by(name: params[:location]) : project.backup_location
    restore = Deploy.request_restore!(project, snapshot: params[:snapshot].to_s, location:, confirm: params[:confirm].to_s)
    render json: RemoteView.deploy(restore), status: :accepted
  rescue Deploy::RestoreRefused => e
    render json: { error: e.message }, status: :unprocessable_entity
  end
end
