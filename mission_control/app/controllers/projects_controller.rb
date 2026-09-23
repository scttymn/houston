class ProjectsController < ApplicationController
  include ProjectPage

  def index
    @projects = Project.order(:name).to_a
    @installation = Installation.current
    @storage = StorageLocation.find_by(default: true)
    @tunnel = SystemStatus.tunnel(@installation)
    @on_lan = request.host != "admin.#{@installation.base_domain}"
    @admin_route = SystemStatus.admin_route(@installation, on_admin: !@on_lan)
  end

  def show
    prepare_project_page(Project.find_by!(name: params[:name]))
  end
end
