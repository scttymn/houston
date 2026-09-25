# houston update: update this server to a release (ServerUpdate), and follow it.
class Api::V1::UpdateController < Api::V1::BaseController
  # GET /api/v1/update: the version it runs, a newer release if one is
  # out, and the last update started from Mission Control.
  def show
    render json: view
  end

  # POST /api/v1/update {version}: version defaults to the latest release.
  def create
    version = params[:version]
    return render json: { error: "version must be a release tag, like v0.4.3" }, status: :bad_request unless version.nil? || version.is_a?(String)

    update = ServerUpdate.start!(version)
    render json: view.merge(message: "Updating to #{update.to_version}. Mission Control restarts on the way; deploys and backups wait."), status: :accepted
  rescue ServerUpdate::Refused => e
    render json: { error: "Houston #{e.message}" }, status: :unprocessable_entity
  end

  private
    def view
      last = ServerUpdate.order(:id).last
      { version: HoustonVersion.current, latest: UpdateNotice.newer(HoustonVersion.current, Installation.current.latest_release),
        update: last && { id: last.id, to: last.to_version, from: last.from_version, status: last.status,
                          started_at: last.started_at, finished_at: last.finished_at, log: last.log } }
    end
end
