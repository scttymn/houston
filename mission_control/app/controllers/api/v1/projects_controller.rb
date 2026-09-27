class Api::V1::ProjectsController < Api::V1::BaseController
  def index
    render json: { projects: Project.order(:name).map { |p| RemoteView.project(p) } }
  end

  def show
    project = Project.find_by(name: params[:name])
    return render json: { error: "no project #{params[:name]}" }, status: :not_found unless project
    render json: RemoteView.project(project, detail: true)
  end

  # {confirm, delete_backups?}: queues the project's deletion
  # (docs/plans/delete-project.md), or resumes one that stopped partway.
  def destroy
    project = Project.find_by(name: params[:name])
    return render json: { error: "no project #{params[:name]}" }, status: :not_found unless project

    deletion = ProjectDeletion.request!(project, confirm: params[:confirm].to_s, delete_backups: params[:delete_backups] == true, by: "token #{@api_token.name}")
    render json: { deletion: RemoteView.deletion(deletion) }, status: :accepted
  rescue ProjectDeletion::Refused => e
    render json: { error: e.message }, status: :unprocessable_entity
  end
end
