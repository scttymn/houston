class ProjectsController < ApplicationController
  def index
    @installation = Installation.current
    @storage = StorageLocation.find_by(default: true)
    @tunnel = SystemStatus.tunnel(@installation)
    @on_lan = request.host != "admin.#{@installation.base_domain}"
    @admin_route = SystemStatus.admin_route(@installation, on_admin: !@on_lan)
  end
end
