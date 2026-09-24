class Api::V1::MeController < Api::V1::BaseController
  # GET /api/v1/me: which token this is, on which server, running which
  # Houston (houston login checks it; houston status shows the version).
  def show
    render json: { token: @api_token.name, server: Installation.current.base_domain, version: HoustonVersion.current }
  end
end
