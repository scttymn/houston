# A project's webhook for the CLI. The secret comes back only until its first
# verified delivery, as on the project page: it has to be pasted into the
# git host, and after that nobody needs to see it.
class Api::V1::WebhooksController < Api::V1::BaseController
  before_action :set_project

  def show
    render json: view
  end

  def rotate
    @project.update!(webhook_secret: SecureRandom.urlsafe_base64(32), webhook_verified_at: nil)
    render json: view
  end

  private
    def view
      { url: "https://hooks.#{Installation.current.base_domain}/#{@project.name}", verified: @project.webhook_verified_at.present?,
        secret: @project.webhook_verified_at ? nil : @project.webhook_secret }
    end

    def set_project
      @project = Project.find_by(name: params[:project_name])
      return render json: { error: "no project #{params[:project_name]}" }, status: :not_found unless @project
      render json: { error: "#{@project.name} isn't linked to a repo (houston link)" }, status: :unprocessable_entity if @project.repo_url.blank?
    end
end
