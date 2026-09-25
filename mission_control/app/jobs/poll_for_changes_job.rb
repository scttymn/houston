# The webhook is a doorbell; this is the fallback when a ring is missed or
# refused (security fixes, M4): every few minutes, the projects that deploy
# on push are checked too. One whose webhook never arrived doesn't deploy on
# push yet, so it isn't polled.
class PollForChangesJob < ApplicationJob
  queue_as :default

  def perform
    Project.where.not(webhook_verified_at: nil).where.not(repo_url: [ nil, "" ]).find_each do |project|
      CheckForChangesJob.perform_later(project.id)
    end
  end
end
