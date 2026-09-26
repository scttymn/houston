# Settings › Releases: check GitHub for a newer Houston, update this server
# to it (ServerUpdate), and each update's log, as a deploy's page shows a
# deploy's. What happened shows as a toast.
class Settings::UpdatesController < ApplicationController
  GITHUB_DOWN = "Couldn't reach GitHub just now; try again in a minute.".freeze

  def show
    @update = ServerUpdate.find(params[:id])
  end

  def check
    if (found = LatestRelease.check_and_say)
      redirect_to settings_path(anchor: "releases"), flash: { toast: { "kind" => "go", "text" => found } }
    else
      redirect_to settings_path(anchor: "releases"), flash: { toast: { "kind" => "nogo", "text" => GITHUB_DOWN } }
    end
  end

  def create
    update = ServerUpdate.start!(params[:version])
    redirect_to settings_update_path(update), flash: { toast: { "kind" => "go", "text" => "Updating to #{update.to_version}. Mission Control restarts on the way; this page comes back when it's up." } }
  rescue ServerUpdate::Refused => e
    redirect_to settings_path(anchor: "releases"), flash: { toast: { "kind" => "nogo", "text" => "No update: Houston #{e.message}." } }
  end
end
