class Api::V1::ProjectsController < Api::V1::BaseController
  def index
    render json: { projects: Project.order(:name).map { |p| RemoteView.project(p) } }
  end

  def show
    project = Project.find_by(name: params[:name])
    return render json: { error: "no project #{params[:name]}" }, status: :not_found unless project
    render json: RemoteView.project(project, detail: true)
  end
end
