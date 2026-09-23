class Api::V1::MeController < Api::V1::BaseController
  # GET /api/v1/me: which token this is, on which server (houston login checks it).
  def show
    render json: { token: @api_token.name, server: Installation.current.base_domain }
  end
end
