# A project's repo URL, changed when the repo moved or was renamed on its
# git host (RepoMove checks it first).
class ProjectReposController < ApplicationController
  def update
    project = Project.find_by!(name: params[:project_name])
    RepoMove.new(project, params[:repo_url]).call!
    redirect_to project_path(project.name, anchor: "webhook"), notice: "#{project.name}'s repo is now #{project.repo_url}."
  rescue RepoMove::Refused => e
    redirect_to project_path(project.name, anchor: "webhook"), alert: e.message
  end
end
