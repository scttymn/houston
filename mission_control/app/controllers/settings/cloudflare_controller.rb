# Settings › Cloudflare (docs/plans/cloudflare-settings.md): the live panel,
# loaded after the page (Cloudflare can be slow), replacing the API token
# (checked before it's saved), and repairing routes and records.
class Settings::CloudflareController < ApplicationController
  include SettingsPage

  def show
    render partial: "settings/cloudflare/live", locals: { view: CloudflareView.fetch }
  end

  def token
    token = CloudflareToken.new(params[:api_token])
    return redirect_to settings_path(anchor: "cloudflare"), notice: "Cloudflare token replaced: it passed every check." if token.replace

    @token_checks = token.checks
    render_settings_page(status: :unprocessable_entity)
  end

  def repair
    @repair_results = CloudflareRepair.run
    render_settings_page
  end
end
