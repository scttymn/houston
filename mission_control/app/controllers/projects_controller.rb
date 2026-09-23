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
    @next_backup = next_backup
    @failed_backups = BackupRun.where(id: BackupRun.group(:project_id).select("MAX(id)")).where(status: "no_go").pluck(:project_id).to_set
  end

  def show
    prepare_project_page(Project.find_by!(name: params[:name]))
  end

  private
    # The soonest scheduled backup across projects that will run one.
    def next_backup
      zone = @installation.zone
      @projects.select { |p| (p.volumes.any? || p.databases.any?) && p.running_deploy && p.backup_location }
               .map { |p| BackupSchedule.new(p, zone).next_at(Time.current) }.min&.in_time_zone(zone)
    end

  public

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
