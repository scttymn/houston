# Settings › API tokens: named tokens for the houston CLI (--server) and agents.
class Settings::TokensController < ApplicationController
  def index
    @tokens = ApiToken.order(:name)
  end

  def create
    @new_token, created = ApiToken.issue!(params[:name].to_s.strip)
    @created_name = created.name
    @tokens = ApiToken.order(:name)
    render :index
  rescue ActiveRecord::RecordInvalid => e
    @error = e.record.errors.full_messages.to_sentence
    @tokens = ApiToken.order(:name)
    render :index, status: :unprocessable_entity
  end

  def destroy
    ApiToken.find(params[:id]).destroy!
    redirect_to settings_tokens_path, notice: "Revoked. Anything using that token stops working now."
  end
end
