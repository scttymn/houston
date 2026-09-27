# What a copy's runner asks of Mission Control (docs/plans/copy-project.md,
# Batch 4), with its deploy's token, while it's in flight: the data
# (copy_data: POST starts it, GET is where it stands), and the handover of
# the hosts the old and new projects share.
class Api::CopiesController < Api::BaseController
  before_action :set_copy_deploy

  def create_data
    render json: CopyData.start!(@deploy), status: :accepted
  end

  def show_data
    render json: CopyData.status(@deploy)
  end

  def handover
    copy = @deploy.copy
    moved = copy.with_lock do
      # A cancel may have ended the deploy since the check above.
      next render(json: { error: "copy ##{@deploy.number} was cancelled" }, status: :conflict) unless @deploy.reload.in_flight?
      Handover.new(copy).forward!
    end
    return if performed?
    point_domains(@deploy.project)
    render json: { handed_over: moved }
  rescue Handover::Failed => e
    render json: { error: e.message }, status: :unprocessable_entity
  end

  private
    def set_copy_deploy
      @deploy = Deploy.find_by(id: params[:id])
      return render json: { error: "no such deploy" }, status: :not_found unless @deploy
      return render json: { error: "that token isn't this deploy's" }, status: :forbidden unless @deploy.owned_by?(request.headers["X-Houston-Deploy-Token"])
      return render json: { error: "deploy ##{@deploy.number} isn't a copy" }, status: :unprocessable_entity unless @deploy.copy? && @deploy.copy
      render json: { error: "copy ##{@deploy.number} is no longer in flight" }, status: :conflict unless @deploy.in_flight?
    end

    # The new project's domains, now its own: their states again. Best
    # effort; its first GO points them as well.
    def point_domains(project)
      installation = Installation.current
      return unless installation.connected? && installation.cloudflare_api_token.present?
      dns = DomainDns.new(project, installation)
      project.update!(domain_states: project.domains.reject { |d| d == project.host }.index_with { |d| dns.point(d).to_h })
    rescue StandardError => e
      Rails.logger.warn("houston: #{project.name}'s domains weren't pointed after its handover: #{e.message}")
    end
end
