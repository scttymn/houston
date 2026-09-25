# The flight board's Update button (ServerUpdate).
class ServerUpdatesController < ApplicationController
  def create
    update = ServerUpdate.start!(params[:version])
    redirect_to root_path, notice: "Updating to #{update.to_version}. Mission Control restarts on the way; this page comes back when it's up."
  rescue ServerUpdate::Refused => e
    redirect_to root_path, alert: "No update: Houston #{e.message}."
  end
end
