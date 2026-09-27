# Copying a project to a new name (docs/plans/copy-project.md): create on
# the old project ({confirm}: its name), cancel and undo ({confirm}: the new
# project's name) on the new one.
class Api::V1::CopiesController < Api::V1::BaseController
  before_action :set_project
  before_action :set_copy, only: %i[ cancel undo ]

  def create
    copy = ProjectCopy.request!(@project, confirm: params[:confirm].to_s, by:)
    render json: { copy: RemoteView.copy(copy) }, status: :accepted
  rescue ProjectCopy::Refused => e
    render json: { error: e.message }, status: :unprocessable_entity
  end

  def cancel
    @copy.cancel!(by:)
    render json: { copy: RemoteView.copy(@copy.reload) }
  rescue ProjectCopy::Refused => e
    render json: { error: e.message }, status: :unprocessable_entity
  end

  def undo
    deletion = @copy.undo!(confirm: params[:confirm].to_s, by:)
    render json: { deletion: RemoteView.deletion(deletion).except(:log), copy: RemoteView.copy(@copy.reload) }, status: :accepted
  rescue ProjectCopy::Refused => e
    render json: { error: e.message }, status: :unprocessable_entity
  end

  private
    def by = "token #{@api_token.name}"

    def set_project
      @project = Project.find_by(name: params[:project_name])
      render json: { error: "no project #{params[:project_name]}" }, status: :not_found unless @project
    end

    def set_copy
      @copy = @project.copies.order(:id).last
      render json: { error: "#{@project.name} isn't a copy" }, status: :not_found unless @copy
    end
end
