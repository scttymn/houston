class ProjectsController < ApplicationController
  def index
    @installation = Installation.current
    @storage = StorageLocation.find_by(default: true)
    @tunnel = SystemStatus.tunnel(@installation)
    @on_lan = request.host != "admin.#{@installation.base_domain}"
  end
end
