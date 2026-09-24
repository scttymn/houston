class Api::V1::BackupsController < Api::V1::BaseController
  before_action :set_project

  # A snapshot now (Create on the Snapshots panel); a backup already queued is returned as it is.
  def create
    render json: RemoteView.backup(BackupRun.request!(@project)), status: :accepted
  rescue BackupRun::Refused => e
    render json: { error: e.message }, status: :unprocessable_entity
  end

  def show
    run = params[:id] == "latest" ? @project.backup_runs.order(:id).last : @project.backup_runs.find_by(id: params[:id])
    return render json: { error: "no backup #{params[:id]} of #{@project.name}" }, status: :not_found unless run
    render json: RemoteView.backup(run)
  end

  private
    def set_project
      @project = Project.find_by(name: params[:project_name])
      render json: { error: "no project #{params[:project_name]}" }, status: :not_found unless @project
    end
end
