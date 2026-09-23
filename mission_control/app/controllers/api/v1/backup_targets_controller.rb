class Api::V1::BackupTargetsController < Api::V1::BaseController
  # {location: <name> | null (the default)}
  def update
    project = Project.find_by(name: params[:project_name])
    return render json: { error: "no project #{params[:project_name]}" }, status: :not_found unless project
    name = params[:location]
    location = name.present? ? StorageLocation.find_by(name:) : nil
    return render json: { error: "no storage location #{name}" }, status: :unprocessable_entity if name.present? && !location

    project.choose_backup_location!(location)
    render json: { backup_location: project.backup_location&.name }
  rescue Project::Refused => e
    render json: { error: e.message }, status: :unprocessable_entity
  end
end
