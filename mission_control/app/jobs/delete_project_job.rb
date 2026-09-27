# Runs one ProjectDeletion (docs/plans/delete-project.md). It has its own
# queue: a final snapshot takes minutes, and never waits behind (or holds up)
# another project's backup or a webhook's change check.
class DeleteProjectJob < ApplicationJob
  queue_as :deletions

  def perform(deletion)
    ProjectRemoval.new(deletion).call
  end
end
