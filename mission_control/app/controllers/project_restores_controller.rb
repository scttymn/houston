# The Restore page: roll a project back to a snapshot, code and data, after
# the admin types its name. A restore is queued like a deploy and shown as one.
class ProjectRestoresController < ApplicationController
  before_action :set_snapshot

  def new; end

  def create
    restore = Deploy.request_restore!(@project, snapshot: params[:snapshot], location: @location, confirm: params[:confirm].to_s)
    redirect_to project_deploy_path(@project.name, restore.number)
  rescue Deploy::RestoreRefused => e
    @error = e.message
    render :new, status: :unprocessable_entity
  end

  private
    def set_snapshot
      @project = Project.find_by!(name: params[:project_name])
      @location = StorageLocation.find_by!(name: params[:location])
      @snapshot = Snapshots.for(@project, @location).find { |s| s.id == params[:snapshot] || s.short_id == params[:snapshot] }
      raise ActiveRecord::RecordNotFound, "no snapshot #{params[:snapshot]}" unless @snapshot
      @running = @project.running_deploy
    rescue Snapshots::Unavailable => e
      redirect_to project_path(@project.name), alert: "Can't read #{@location.name}'s snapshots: #{e.message}"
    end
end
