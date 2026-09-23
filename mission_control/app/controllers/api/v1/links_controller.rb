# Add project over the API (houston link): the same draft, checks and save
# as the page, on the same models.
class Api::V1::LinksController < Api::V1::BaseController
  before_action :set_link, except: :create

  def create
    url = json_param("repo_url").to_s.strip
    candidate = RepoLink.new(repo_url: url, deploy_key_private: "-", deploy_key_public: "-")
    return render json: { error: "the repo URL #{candidate.errors[:repo_url].to_sentence}" }, status: :unprocessable_entity unless candidate.valid?

    link = RepoLink.start!(url)
    access = GitRemote.check(link)
    render json: { id: link.id, deploy_key: link.deploy_key_public, access: { ok: access.ok, message: access.message } }
  end

  def access
    result = GitRemote.check(@link)
    render json: { ok: result.ok, message: result.message }
  end

  def read
    @link.assign_attributes(branch: json_param("branch").presence || @link.branch, compose_path: json_param("compose_path").presence || @link.compose_path)
    return render json: { error: @link.errors.full_messages.to_sentence }, status: :unprocessable_entity unless @link.valid?

    result = GitRemote.read(@link)
    @link.update!(preview: result.inspection, preview_sha: result.sha)
    if result.ok
      render json: { ok: true, found: { name: result.inspection.dig("sync", "name"), sha: result.sha, branch: @link.branch,
                                       domains: result.inspection.dig("sync", "domains"), variables: result.inspection.dig("sync", "variables") } }
    else
      render json: { ok: false, problems: result.problems }
    end
  end

  def save
    project = ProjectLinking.new(@link).save!
    render json: { project: project.name, webhook_url: "https://hooks.#{Installation.current.base_domain}/#{project.name}",
                   webhook_secret: project.webhook_verified_at ? nil : project.webhook_secret }
  rescue ProjectLinking::Refused => e
    render json: { error: e.message }, status: :unprocessable_entity
  end

  private
    def set_link
      @link = RepoLink.find_by(id: params[:id])
      render json: { error: "no link #{params[:id]} (drafts last a day)" }, status: :not_found unless @link
    end

    def json_param(key)
      @body ||= (JSON.parse(request.body.read(64.kilobytes).presence || "{}") rescue {})
      @body[key]
    end
end
