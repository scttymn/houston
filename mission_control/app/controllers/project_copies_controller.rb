# Copying a project to the new name its compose.yml gives
# (docs/plans/copy-project.md): the confirm page and asking, on the old
# project; Cancel and Undo, on the new one.
class ProjectCopiesController < ApplicationController
  before_action :set_old, only: %i[ new create ]
  before_action :set_new, only: %i[ cancel undo ]

  def new; end

  def create
    copy = ProjectCopy.request!(@project, confirm: params[:confirm].to_s, by: Current.session.user.email_address)
    redirect_to project_deploy_path(copy.to, copy.deploy.number)
  rescue ProjectCopy::Refused => e
    @error = e.message
    render :new, status: :unprocessable_entity
  end

  def cancel
    @copy.cancel!(by: Current.session.user.email_address)
    redirect_to project_deploy_path(@project.name, @copy.deploy.number)
  rescue ProjectCopy::Refused => e
    redirect_to project_deploy_path(@project.name, @copy.deploy.number), alert: e.message
  end

  def undo
    deletion = @copy.undo!(confirm: params[:confirm].to_s, by: Current.session.user.email_address)
    redirect_to deletion_path(deletion)
  rescue ProjectCopy::Refused => e
    redirect_to project_path(@project.name), alert: e.message
  end

  private
    def set_old
      @project = Project.find_by!(name: params[:project_name])
      @installation = Installation.current
      @proposal = @project.copy_proposal
      @to = @proposal&.proposed_name
      @refusal = @to ? Project.name_refusal(@to) : "#{@project.name}'s latest deploy doesn't propose a copy"
      @storage = @project.backup_location
    end

    def set_new
      @project = Project.find_by!(name: params[:project_name])
      @copy = @project.copies.order(:id).last or raise ActiveRecord::RecordNotFound, "#{@project.name} isn't a copy"
    end
end
