# The remote API: for the houston CLI with --server and for agents, with a
# named personal token (Settings › API tokens). It works through the tunnel.
# It never returns a secret's value or a deploy key; those stay on the
# runner's local API (Api::BaseController), which personal tokens can't use.
class Api::V1::BaseController < ActionController::API
  before_action :authenticate, :require_setup

  private
    def authenticate
      @api_token = ApiToken.authenticate(request.authorization.to_s[/\ABearer (.+)\z/, 1])
      return render(json: { error: "the API token is missing, wrong or revoked" }, status: :unauthorized) unless @api_token
      @api_token.used!
    end

    def require_setup
      render json: { error: "finish setup in Mission Control first" }, status: :conflict unless Installation.connected?
    end
end
