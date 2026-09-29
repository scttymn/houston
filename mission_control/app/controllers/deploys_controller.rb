class DeploysController < ApplicationController
  def show
    @project = Project.find_by!(name: params[:project_name])
    @deploy = @project.deploys.find_by!(number: params[:number])
    @running = @project.running_deploy
  end

  # Deploy: the head of what the deploy rule matches, as houston deploy
  # --server does. A deploy already queued moves to it (Deploy.queue!).
  # fresh=1: Rebuild, as houston rebuild --server (docs/plans/rebuild.md).
  def create
    project = Project.find_by!(name: params[:project_name])
    return redirect_to project_path(project.name), alert: "Link the repo first (Add project or houston link): a deploy fetches its commit from it." if project.repo_url.blank?

    deploy = ChangeCheck.new(project).queue_head!(fresh: params[:fresh].present?)
    redirect_to project_deploy_path(project.name, deploy.number)
  rescue ChangeCheck::Failed => e
    redirect_to project_path(project.name), alert: "Can't deploy: #{e.message}"
  end
end
