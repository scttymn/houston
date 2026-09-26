# Settings › Security, port 3000: close it (bound to 127.0.0.1) or open it to the
# network (PortSwitch). Closing it from port 3000 itself goes on to
# admin.<base> first, so the page isn't the one that disappears.
class Settings::PortController < ApplicationController
  def update
    open = params[:open] == "1"
    PortSwitch.set!(open:)
    base = Installation.current.base_domain
    if !open && !through_tunnel? && base.present?
      redirect_to "https://admin.#{base}#{settings_path(anchor: "security")}", allow_other_host: true
    else
      redirect_to settings_path(anchor: "security"), notice: "Port 3000 is #{open ? "opening to your network" : "closing to the network"}. Mission Control restarts for a few seconds."
    end
  rescue PortSwitch::Refused => e
    redirect_to settings_path(anchor: "security"), alert: "Port 3000 is unchanged: Houston #{e.message}."
  end
end
