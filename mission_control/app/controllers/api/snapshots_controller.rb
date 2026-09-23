# The pre-deploy snapshot, asked for by the deploy that owns it (its token in
# X-Houston-Deploy-Token, like progress reports), while it's in flight. One
# per deploy: a retried POST gets the same run. A restore's is its safety
# snapshot (reason restore), taken just before the switch.
class Api::SnapshotsController < Api::BaseController
  before_action :set_deploy

  def create
    run = BackupRun.request!(@deploy.project, reason:, deploy_number: @deploy.number)
    render json: RemoteView.backup(run), status: :accepted
  rescue BackupRun::Refused => e
    return render json: { status: "skipped", error: e.message } if e.message == BackupRun::NOTHING_DEPLOYED
    # 409 means "no longer in flight" to the runner (it stops, taken over).
    render json: { error: e.message }, status: :unprocessable_entity
  end

  def show
    run = @deploy.project.backup_runs.find_by(operation: "backup", reason:, deploy_number: @deploy.number)
    return render json: { error: "deploy ##{@deploy.number} has no snapshot" }, status: :not_found unless run
    render json: RemoteView.backup(run)
  end

  private
    def reason = @deploy.restore? ? "restore" : "deploy"

    def set_deploy
      @deploy = Deploy.find_by(id: params[:id])
      return render json: { error: "no such deploy" }, status: :not_found unless @deploy
      return render json: { error: "that token isn't this deploy's" }, status: :forbidden unless @deploy.owned_by?(request.headers["X-Houston-Deploy-Token"])
      render json: { error: "deploy ##{@deploy.number} is no longer in flight" }, status: :conflict unless @deploy.in_flight?
    end
end
