class CheckForChangesJob < ApplicationJob
  queue_as :default
  # One at a time per project: a webhook's check and the poll's can't
  # interleave (the one holding older refs would queue an older commit).
  limits_concurrency to: 1, key: ->(project_id) { project_id }, duration: 5.minutes

  def perform(project_id)
    project = Project.find_by(id: project_id)
    ChangeCheck.new(project).run if project&.repo_url.present?
  end
end
