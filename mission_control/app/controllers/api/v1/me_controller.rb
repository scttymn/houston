class Api::V1::MeController < Api::V1::BaseController
  # GET /api/v1/me: which token this is, on which server, running which
  # Houston (houston login checks it; houston status shows the version, and
  # an update running).
  def show
    installation = Installation.current
    render json: { token: @api_token.name, server: installation.base_domain, version: HoustonVersion.current,
                   latest: UpdateNotice.newer(HoustonVersion.current, installation.latest_release),
                   updating: ServerUpdate.running.first&.to_version }
  end
end
