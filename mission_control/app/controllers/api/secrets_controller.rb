class Api::SecretsController < Api::BaseController
  # GET /api/projects/:name/secrets/:key: one value, for houston deploy to
  # hand to Kamal. Only keys the project's compose.yml references.
  def show
    project = Project.find_by(name: params[:name])
    secret = project&.variable?(params[:key]) && project.secrets.find_by(key: params[:key])
    if secret && secret.value.present?
      render plain: secret.value
    else
      render json: { error: "no value for #{params[:key]}" }, status: :not_found
    end
  end
end
