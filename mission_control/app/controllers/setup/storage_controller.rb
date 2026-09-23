# First-run step 3.
class Setup::StorageController < ApplicationController
  skip_before_action :require_setup
  before_action :cloudflare_first
  before_action :closed_once_done
  # These pages show the restic password; browsers mustn't keep them.
  before_action :no_store

  def show
    @location = StorageLocation.setup_candidate
    @setup = StorageSetup.new(kind: "nfs")
  end

  def create
    @setup = StorageSetup.new(params.expect(storage: StorageSetup.attribute_names.map(&:to_sym)))
    if @setup.save
      redirect_to setup_storage_path
    else
      render :show, status: :unprocessable_entity
    end
  end

  def finish
    @location = StorageLocation.setup_candidate
    return head :not_found unless @location

    if params[:saved] == "1"
      StorageLocation.transaction do
        StorageLocation.where.not(id: @location.id).update_all(default: false)
        @location.update!(acknowledged_at: Time.current, default: true)
      end
      redirect_to root_path
    else
      @setup = StorageSetup.new(kind: @location.kind)
      @unsaved = true
      render :show, status: :unprocessable_entity
    end
  end

  def password
    location = StorageLocation.setup_candidate
    return head :not_found unless location

    send_data "#{location.restic_password}\n", filename: "houston-#{location.name}-restic-password.txt",
              type: "text/plain", disposition: "attachment"
  end

  private
    def cloudflare_first
      redirect_to setup_cloudflare_path unless Installation.connected?
    end

    def closed_once_done
      redirect_to root_path if StorageLocation.default_ready?
    end
end
