# Houston's own page (/update): the version it runs, a check for a newer one,
# updating to it (ServerUpdate), and each update's steps and log, as a deploy
# page shows a deploy's.
class ServerUpdatesController < ApplicationController
  GITHUB_DOWN = "Couldn't reach GitHub just now; try again in a minute.".freeze

  def show
    @installation = Installation.current
    @updates = ServerUpdate.order(id: :desc).limit(10).to_a
    # The newest, running or not (one runs at a time, and it's the newest).
    @update = params[:id] ? ServerUpdate.find(params[:id]) : @updates.first
    @newer = UpdateNotice.newer(HoustonVersion.current, @installation.latest_release)
  end

  def check
    if (found = LatestRelease.check_and_say)
      redirect_to server_update_path, notice: found
    else
      redirect_to server_update_path, alert: GITHUB_DOWN
    end
  end

  def create
    update = ServerUpdate.start!(params[:version])
    redirect_to server_update_path, notice: "Updating to #{update.to_version}. Mission Control restarts on the way; this page comes back when it's up."
  rescue ServerUpdate::Refused => e
    redirect_to server_update_path, alert: "No update: Houston #{e.message}."
  end
end
