# Mission Control's local API, for houston deploy on the server and (later)
# the runners. Answers only the runner token, and never through the tunnel:
# Cloudflare adds Cf-Ray and Cf-Connecting-Ip to every request it forwards,
# and a client can't remove them.
class Api::BaseController < ActionController::API
  MAX_BODY = 64.kilobytes

  wrap_parameters false
  before_action :refuse_tunnel, :authenticate, :require_setup

  private
    def refuse_tunnel
      head :not_found if request.headers["Cf-Ray"].present? || request.headers["Cf-Connecting-Ip"].present?
    end

    def authenticate
      expected = ENV["HOUSTON_RUNNER_TOKEN"].to_s
      given = request.authorization.to_s[/\ABearer (.+)\z/, 1].to_s
      unless expected.length >= 32 && ActiveSupport::SecurityUtils.secure_compare(given, expected)
        render json: { error: "the runner token is missing or wrong" }, status: :unauthorized
      end
    end

    def require_setup
      render json: { error: "finish setup in Mission Control first" }, status: :conflict unless Installation.connected?
    end

    # The request body as JSON, read with a size limit. Renders and returns
    # nil when it's too large or not JSON.
    def json_body(limit: MAX_BODY)
      body = request.body.read(limit + 1).to_s
      return render(json: { error: "the request is larger than #{limit / 1.kilobyte} KiB" }, status: :content_too_large) && nil if body.bytesize > limit
      JSON.parse(body)
    rescue JSON::ParserError
      render(json: { error: "the request isn't JSON" }, status: :bad_request) && nil
    end
end
