# The registry's garbage collection after a deletion (docs/plans/delete-project.md,
# Batch 7). While a deploy is in flight it waits and tries again; how it went
# goes in the deletion's log. It never changes the deletion: the project is
# already gone, and any later cleanup frees whatever is left.
class RegistryCleanupJob < ApplicationJob
  WAIT = 30.seconds
  WAIT_FOR = 6.hours

  queue_as :deletions
  retry_on RegistryCleanup::Busy, wait: WAIT, attempts: (WAIT_FOR / WAIT).ceil do |job, error|
    job.note(job.arguments.first, "registry space wasn't freed: waited #{WAIT_FOR.inspect} (#{error.message})")
  end

  def perform(deletion)
    freed = RegistryCleanup.run!
    note(deletion, "ok  registry space freed#{" (#{freed})" if freed}")
  rescue RegistryCleanup::Failed => e
    note(deletion, "registry space wasn't freed: #{e.message}")
  end

  def note(deletion, line)
    ProjectDeletion.where(id: deletion.id).update_all([ "log = COALESCE(log, '') || ?, updated_at = ?", "#{line}\n", Time.current ])
  end
end
