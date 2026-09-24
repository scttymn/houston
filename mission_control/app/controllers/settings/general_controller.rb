# Settings › Time zone (backup schedules run in it). The section lives on the Settings page.
class Settings::GeneralController < ApplicationController
  include SettingsPage

  def show
    redirect_to settings_path(anchor: "cloudflare")
  end

  def update
    installation = Installation.current
    if installation.update(time_zone: params[:time_zone].to_s)
      redirect_to settings_path(anchor: "time-zone"), notice: "Houston's time zone is #{installation.time_zone}."
    else
      @error = installation.errors.full_messages.to_sentence
      render_settings_page(status: :unprocessable_entity)
    end
  end
end
