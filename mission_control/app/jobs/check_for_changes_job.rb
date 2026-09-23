class CheckForChangesJob < ApplicationJob
  queue_as :default

  def perform(project_id)
    project = Project.find_by(id: project_id)
    ChangeCheck.new(project).run if project&.repo_url.present?
  end
end
