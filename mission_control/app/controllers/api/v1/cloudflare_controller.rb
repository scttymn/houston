# Cloudflare over the remote API (houston cloudflare): the view, replacing the
# token (write-only: it's never returned), and repair.
class Api::V1::CloudflareController < Api::V1::BaseController
  def show
    render json: CloudflareView.fetch.to_h
  end

  def token
    token = CloudflareToken.new(params[:token])
    replaced = token.replace
    render json: { replaced:, checks: token.checks.map(&:to_h) }, status: replaced ? :ok : :unprocessable_entity
  end

  def repair
    render json: { results: CloudflareRepair.run.map(&:to_h) }
  end
end
