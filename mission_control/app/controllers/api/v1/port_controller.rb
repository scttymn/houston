# houston port / port open / port close: Settings › Security (PortSwitch).
class Api::V1::PortController < Api::V1::BaseController
  def show
    render json: view
  end

  def update
    open = params[:open]
    return render json: { error: "open must be true or false" }, status: :bad_request unless [ true, false ].include?(open)

    PortSwitch.set!(open:)
    render json: view.merge(message: "Port 3000 is #{open ? "opening to the network" : "closing to the network"}. Mission Control restarts for a few seconds."), status: :accepted
  rescue PortSwitch::Refused => e
    render json: { error: "Houston #{e.message}" }, status: :unprocessable_entity
  end

  private
    # open/address: what it's bound to now (nil when unknown); saved: the
    # choice the installer keeps.
    def view
      address = PortExposure.address
      { open: address && address != "127.0.0.1", address:, saved: Installation.current.port_open ? "open" : "closed" }
    end
end
