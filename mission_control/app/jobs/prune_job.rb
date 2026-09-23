# Once a day: restic prune on each location Houston has backed up to, which
# frees the space of the snapshots forget dropped. On the backups queue, so
# it never overlaps one of Houston's backups; --retry-lock waits out others.
class PruneJob < ApplicationJob
  queue_as :backups

  TIMEOUT = 3.hours

  def perform
    used = BackupRun.distinct.select(:location_id)
    StorageLocation.where(id: used).where.not(acknowledged_at: nil).order(:id).find_each do |location|
      ran = DockerCommand.run(*location.restic_args("prune", "--retry-lock", "30m"), env: location.restic_env, timeout: TIMEOUT.to_i)
      if ran.success
        location.update!(pruned_at: Time.current, prune_error: nil)
      else
        error = ran.output.to_s.lines.last(5).join.strip.truncate(2000)
        location.update!(prune_error: error)
        Rails.logger.error("prune of #{location.name} failed: #{error}")
      end
    end
  end
end
