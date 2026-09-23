class DeploysController < ApplicationController
  def show
    @project = Project.find_by!(name: params[:project_name])
    @deploy = @project.deploys.find_by!(number: params[:number])
    @running = @project.running_deploy
  end
end
