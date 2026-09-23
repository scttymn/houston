class Api::V1::VolumesController < Api::V1::BaseController
  before_action :set_project

  def index
    render json: { volumes: @project.volume_rows.map { |volume, row| view(volume, row) } }
  end

  # {location: <a location's name> | null (local disk)}
  def update
    name = params[:location]
    location = name.present? ? StorageLocation.find_by(name:) : nil
    return render json: { error: "no storage location #{name}" }, status: :unprocessable_entity if name.present? && !location

    row = ProjectVolume.choose!(@project, params[:name], location)
    render json: view(@project.volumes.find { |v| v["name"] == row.name }, row)
  rescue ActiveRecord::RecordNotFound => e
    render json: { error: e.message }, status: :not_found
  rescue ProjectVolume::Refused => e
    render json: { error: e.message }, status: :unprocessable_entity
  rescue ProjectVolume::Placed => e
    render json: { error: e.message }, status: :conflict
  end

  private
    def set_project
      @project = Project.find_by(name: params[:project_name])
      render json: { error: "no project #{params[:project_name]}" }, status: :not_found unless @project
    end

    def view(volume, row) = { name: volume["name"], path: volume["path"], location: row&.location&.name, placed: row&.placed_at.present? }
end
