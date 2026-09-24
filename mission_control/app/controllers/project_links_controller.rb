# Add project: link a repo, check access with its deploy key, read its
# compose file with houston inspect, and save. The draft lives in the
# session until Save.
class ProjectLinksController < ApplicationController
  before_action :set_link

  def new
  end

  def access
    url = params[:repo_url].to_s.strip
    candidate = RepoLink.new(repo_url: url, deploy_key_private: "-", deploy_key_public: "-")
    unless candidate.valid?
      @url_error = candidate.errors[:repo_url].to_sentence
      @entered_url = url
      return render :new, status: :unprocessable_entity
    end

    @link = @link&.repo_url == url ? @link : RepoLink.start!(url)
    session[:repo_link_id] = @link.id
    @access = GitRemote.check(@link)
    render :new
  end

  def read
    return redirect_to link_path unless @link

    @link.assign_attributes(branch: params[:branch].to_s.strip, compose_path: params[:compose_path].to_s.strip)
    return render :new, status: :unprocessable_entity unless @link.valid?

    result = GitRemote.read(@link)
    # A draft from before drafts made webhook secrets gets one now (step 04 shows it).
    @link.update!(preview: result.inspection, preview_sha: result.sha, webhook_secret: @link.webhook_secret.presence || SecureRandom.urlsafe_base64(32))
    @problems = result.problems
    render :new, status: result.ok ? :ok : :unprocessable_entity
  end

  # Save, or Deploy: save, then deploy the head the deploy rule matches.
  def create
    project = ProjectLinking.new(@link, secrets: params.fetch(:secrets, {}).permit!.to_h).save!
    session.delete(:repo_link_id)
    return redirect_to project_path(project.name), notice: "#{project.name} is linked to #{project.repo_url}." if params[:deploy].blank?

    deploy = ChangeCheck.new(project).queue_head!
    redirect_to project_deploy_path(project.name, deploy.number)
  rescue ChangeCheck::Failed => e
    redirect_to project_path(project.name), alert: "#{project.name} is linked, but it can't deploy yet: #{e.message}"
  rescue ProjectLinking::SecretsRefused => e
    @secret_errors = e.errors
    render :new, status: :unprocessable_entity
  rescue ProjectLinking::Refused => e
    @save_error = e.message
    render :new, status: :unprocessable_entity
  end

  private
    def set_link
      @link = RepoLink.find_by(id: session[:repo_link_id])
      @installation = Installation.current
    end
end
