# Storage locations over the remote API: never a password or a credential.
# Adding one (which shows its password once) stays on the Settings page.
class Api::V1::StorageController < Api::V1::BaseController
  def index
    render json: { locations: StorageLocation.where.not(verified_at: nil).order(:name).map { |l| view(l) } }
  end

  # {default: true}
  def update
    location = StorageLocation.find_by(name: params[:name])
    return render json: { error: "no storage location #{params[:name]}" }, status: :not_found unless location
    return render json: { error: "#{location.name} isn't set up yet" }, status: :unprocessable_entity unless location.acknowledged?
    return render json: { error: "send {default: true}" }, status: :unprocessable_entity unless params[:default] == true

    StorageLocation.transaction do
      StorageLocation.where.not(id: location.id).update_all(default: false)
      location.update!(default: true)
    end
    render json: view(location)
  end

  private
    def view(location)
      { name: location.name, kind: location.kind, where: location.where_it_is, live: location.live?, default: location.default?,
        confirmed: location.acknowledged?, used_by: location.projects_using, last_write: location.last_write,
        pruned_at: location.pruned_at, prune_error: location.prune_error }
    end
end
