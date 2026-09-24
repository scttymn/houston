# Settings › API tokens: named tokens for the houston CLI (--server) and agents.
# The section lives on the Settings page; a new token is shown there, once.
class Settings::TokensController < ApplicationController
  include SettingsPage

  def index
    redirect_to settings_path(anchor: "tokens")
  end

  def create
    @new_token, created = ApiToken.issue!(params[:name].to_s.strip)
    @created_name = created.name
    render_settings_page
  rescue ActiveRecord::RecordInvalid => e
    @token_error = e.record.errors.full_messages.to_sentence
    render_settings_page(status: :unprocessable_entity)
  end

  def destroy
    ApiToken.find(params[:id]).destroy!
    redirect_to settings_path(anchor: "tokens"), notice: "Revoked. Anything using that token stops working now."
  end
end
