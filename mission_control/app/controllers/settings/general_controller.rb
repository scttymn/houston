# Settings › General: Houston's time zone (backup schedules run in it).
class Settings::GeneralController < ApplicationController
  def show
    @installation = Installation.current
  end

  def update
    @installation = Installation.current
    if @installation.update(time_zone: params[:time_zone].to_s)
      redirect_to settings_general_path, notice: "Houston's time zone is #{@installation.time_zone}."
    else
      @error = @installation.errors.full_messages.to_sentence
      @installation.time_zone = @installation.time_zone_was
      render :show, status: :unprocessable_entity
    end
  end
end
