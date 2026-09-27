# A copy that failed or was cancelled before its handover: the new project
# goes (docs/plans/copy-project.md), with nothing kept, since it never
# served. A delete is refused while its data is still being restored, so
# it's tried again until that's done.
class CopyCleanupJob < ApplicationJob
  class Busy < StandardError; end

  WAIT = 30.seconds
  queue_as :deletions
  retry_on Busy, wait: WAIT, attempts: 60 do |job, error|
    copy = job.arguments.first
    copy.update!(error: "#{copy.error}; #{copy.to} wasn't removed (#{error.message}): delete it by hand")
  end

  def perform(copy)
    project = copy.project or return
    return if project.deleting? || copy.handed_over_at

    why = copy.error.to_s.start_with?("cancelled") ? "was cancelled" : "failed"
    ProjectDeletion.request!(project, confirm: project.name, delete_backups: true, by: "Houston (the copy of #{copy.from} #{why})")
  rescue ProjectDeletion::Refused => e
    raise Busy, e.message
  end
end
