# Settings › Storage (its list is a section of the Settings page): adding one (the first-run
# form and check), its password shown once until confirmed, and the default.
class Settings::StorageController < ApplicationController
  # The location's page shows the restic password; browsers mustn't keep it.
  before_action :no_store, only: %i[ show password acknowledge ]
  before_action :set_unconfirmed, only: %i[ show password acknowledge ]

  def index
    redirect_to settings_path(anchor: "storage")
  end

  def new
    @setup = StorageSetup.new(kind: "nfs")
  end

  def create
    @setup = StorageSetup.new(params.expect(storage: StorageSetup.attribute_names.map(&:to_sym)))
    if @setup.save
      redirect_to settings_storage_location_path(@setup.location.name)
    else
      render :new, status: :unprocessable_entity
    end
  end

  def show; end

  def password
    send_data "#{@location.restic_password}\n", filename: "houston-#{@location.name}-restic-password.txt", type: "text/plain", disposition: "attachment"
  end

  def acknowledge
    if params[:saved] == "1"
      @location.update!(acknowledged_at: Time.current)
      redirect_to settings_path(anchor: "storage"), notice: "#{@location.name} is ready for backups#{" and live volumes" if @location.live?}."
    else
      @unsaved = true
      render :show, status: :unprocessable_entity
    end
  end

  def make_default
    location = StorageLocation.find_by!(name: params[:name])
    return redirect_to settings_path(anchor: "storage"), alert: "#{location.name} isn't set up yet" unless location.acknowledged?

    StorageLocation.transaction do
      StorageLocation.where.not(id: location.id).update_all(default: false)
      location.update!(default: true)
    end
    redirect_to settings_path(anchor: "storage"), notice: "#{location.name} is the default: projects without their own target back up there."
  end

  private
    # A location's password page exists only until it's confirmed.
    def set_unconfirmed
      @location = StorageLocation.find_by!(name: params[:name])
      redirect_to settings_path(anchor: "storage") if @location.acknowledged? || !@location.verified?
    end
end
