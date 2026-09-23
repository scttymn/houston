class Api::V1::SettingsController < Api::V1::BaseController
  def show
    render json: view(Installation.current)
  end

  def update
    installation = Installation.current
    if installation.update(time_zone: params[:time_zone].to_s)
      render json: view(installation)
    else
      render json: { error: installation.errors.full_messages.to_sentence }, status: :unprocessable_entity
    end
  end

  private
    def view(installation) = { base_domain: installation.base_domain, time_zone: installation.time_zone }
end
