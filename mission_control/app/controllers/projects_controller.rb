class ProjectsController < ApplicationController
  include ProjectPage

  def index
    @projects = Project.order(:name).to_a
    @installation = Installation.current
    @storage = StorageLocation.find_by(default: true)
    @tunnel = SystemStatus.tunnel(@installation)
    @on_lan = request.host != "admin.#{@installation.base_domain}"
    @admin_route = SystemStatus.admin_route(@installation, on_admin: !@on_lan)
    @hooks_route = SystemStatus.route(@installation, "hooks")
  end

  def show
    prepare_project_page(Project.find_by!(name: params[:name]))
  end

  # Check for changes, now (the webhook does the same through a job).
  def check
    project = Project.find_by!(name: params[:name])
    queued = ChangeCheck.new(project).run
    notice = if project.last_check_error then "Couldn't read the repo: #{project.last_check_error}"
    elsif queued.any? then "Queued #{queued.map { |d| "#{d.short_sha} (#{d.ref})" }.to_sentence}."
    else "Nothing new to deploy."
    end
    redirect_to project_path(project.name), notice:
  end

  # A new webhook secret, shown until its first verified delivery.
  def rotate_webhook
    project = Project.find_by!(name: params[:name])
    project.update!(webhook_secret: SecureRandom.urlsafe_base64(32), webhook_verified_at: nil)
    redirect_to project_path(project.name), notice: "New webhook secret. Paste it into the repo's webhook settings."
  end
end
