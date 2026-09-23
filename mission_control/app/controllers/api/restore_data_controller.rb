# A restore deploy's data, put back into the generation it builds, asked for
# by the deploy that owns it (its token in X-Houston-Deploy-Token) while it's
# in flight. One per restore: a retried POST gets the same run.
class Api::RestoreDataController < Api::BaseController
  before_action :set_restore

  def create
    render json: RemoteView.backup(BackupRun.request_restore!(@restore)), status: :accepted
  end

  def show
    run = @restore.project.backup_runs.find_by(operation: "restore", deploy_number: @restore.number)
    return render json: { error: "restore ##{@restore.number} hasn't asked for its data" }, status: :not_found unless run
    render json: RemoteView.backup(run)
  end

  private
    def set_restore
      @restore = Deploy.find_by(id: params[:id])
      return render json: { error: "no such deploy" }, status: :not_found unless @restore
      return render json: { error: "that token isn't this deploy's" }, status: :forbidden unless @restore.owned_by?(request.headers["X-Houston-Deploy-Token"])
      return render json: { error: "deploy ##{@restore.number} isn't a restore" }, status: :unprocessable_entity unless @restore.restore?
      render json: { error: "restore ##{@restore.number} is no longer in flight" }, status: :conflict unless @restore.in_flight?
    end
end
