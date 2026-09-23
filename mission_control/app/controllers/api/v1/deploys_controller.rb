class Api::V1::DeploysController < Api::V1::BaseController
  PER_PAGE = 20

  before_action :set_project

  def index
    page = [ params[:page].to_i, 1 ].max
    deploys = @project.deploys.summary.order(number: :desc).offset((page - 1) * PER_PAGE).limit(PER_PAGE)
    render json: { deploys: deploys.map { |d| RemoteView.deploy(d) } }
  end

  def show
    deploy = params[:number] == "latest" ? @project.deploys.order(number: :desc).first : @project.deploys.find_by(number: params[:number])
    return render json: { error: "no deploy ##{params[:number]} of #{@project.name}" }, status: :not_found unless deploy
    render json: RemoteView.deploy_with_log(deploy, params[:log_from])
  end

  # POST: deploy now (the head of what the deploy rule matches).
  def create
    return render json: { error: "link the repo first (houston link)" }, status: :unprocessable_entity if @project.repo_url.blank?
    deploy = ChangeCheck.new(@project).queue_head!
    render json: RemoteView.deploy(deploy)
  rescue ChangeCheck::Failed => e
    render json: { error: e.message }, status: :bad_gateway
  end

  private
    def set_project
      @project = Project.find_by(name: params[:project_name])
      render json: { error: "no project #{params[:project_name]}" }, status: :not_found unless @project
    end
end
