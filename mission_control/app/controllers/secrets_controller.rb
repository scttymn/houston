# Secret values from the project page. Write-only: nothing here ever renders
# a value, and only variables the project's compose.yml references can be set.
class SecretsController < ApplicationController
  include ProjectPage

  before_action :set_project_and_key

  def update
    secret = @project.secrets.find_or_initialize_by(key: @key)
    secret.value = params[:value]
    if secret.save
      redirect_to project_path(@project.name), notice: "#{@key} saved."
    else
      prepare_project_page(@project, secret_errors: { @key => secret.errors[:value].to_sentence })
      render "projects/show", status: :unprocessable_entity
    end
  end

  # A value nobody needs to know (spec §7), e.g. POSTGRES_PASSWORD.
  def generate
    secret = @project.secrets.find_or_initialize_by(key: @key)
    secret.update!(value: SecureRandom.urlsafe_base64(32))
    redirect_to project_path(@project.name), notice: "#{@key} generated and saved."
  end

  def destroy
    @project.secrets.where(key: @key).delete_all
    redirect_to project_path(@project.name), notice: "#{@key} removed."
  end

  private
    def set_project_and_key
      @project = Project.find_by!(name: params[:project_name])
      @key = params[:key]
      head :not_found unless @project.variable?(@key)
    end
end
