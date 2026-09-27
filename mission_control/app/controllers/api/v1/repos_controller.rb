class Api::V1::ReposController < Api::V1::BaseController
  # {repo_url}: the project's repo moved or was renamed (RepoMove).
  def update
    project = Project.find_by(name: params[:project_name])
    return render json: { error: "no project #{params[:project_name]}" }, status: :not_found unless project

    RepoMove.new(project, params[:repo_url]).call!
    render json: { repo_url: project.repo_url }
  rescue RepoMove::Refused => e
    render json: { error: e.message }, status: :unprocessable_entity
  end
end
