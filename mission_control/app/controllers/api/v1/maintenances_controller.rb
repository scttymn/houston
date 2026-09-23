class Api::V1::MaintenancesController < Api::V1::BaseController
  # {on: true|false, message?}
  def update
    project = Project.find_by(name: params[:project_name])
    return render json: { error: "no project #{params[:project_name]}" }, status: :not_found unless project

    if params[:on] == true
      Maintenance.on!(project, by: "token #{@api_token.name}", message: params[:message])
    else
      Maintenance.off!(project)
    end
    render json: RemoteView.maintenance(project.reload)
  rescue Maintenance::Failed => e
    render json: { error: e.message }, status: :bad_gateway
  rescue ActiveRecord::RecordInvalid => e
    render json: { error: e.record.errors.full_messages.to_sentence }, status: :unprocessable_entity
  end
end
