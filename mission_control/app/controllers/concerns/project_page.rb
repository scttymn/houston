# What the project page needs; secrets#update renders it again with errors.
module ProjectPage
  PER_PAGE = 10

  private
    def prepare_project_page(project, secret_errors: {})
      @project = project
      @installation = Installation.current
      @page = [ params[:page].to_i, 1 ].max
      @deploy_count = project.deploys.count
      @deploys = project.deploys.summary.order(number: :desc).offset((@page - 1) * PER_PAGE).limit(PER_PAGE)
      @running = project.running_deploy
      @secrets = Secret.where(project_id: project.id).index_by(&:key) # only saved values, never a failed attempt
      @variables = project.variables.sort_by { |v| [ v["required"] ? 0 : 1, v["name"] ] }
      @missing = project.missing_secrets
      @secret_errors = secret_errors
    end
end
