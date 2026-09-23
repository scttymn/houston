# Runs one BackupRun. Claims it first (a second delivery of the same job
# finds it claimed and does nothing); waits and tries again while the
# project has another backup running, for as long as one may run.
class BackupJob < ApplicationJob
  class Busy < StandardError; end

  WAIT = 30.seconds

  # A deploy or restore waits for these (its snapshot, its data): they
  # never queue behind another project's long backup.
  queue_as { arguments.first.reason.in?(%w[deploy restore]) ? :snapshots : :backups }
  retry_on Busy, wait: WAIT, attempts: ((Backup::DEADLINE + BackupRun::STALE_AFTER) / WAIT).ceil + 1 do |job, _error|
    job.arguments.first.give_up!("waited #{(Backup::DEADLINE + BackupRun::STALE_AFTER).inspect} for the project's running backup")
  end

  def perform(run)
    token = BackupRun.claim!(run)
    raise Busy, "#{run.project.name} is already backing up" if token == :busy
    (run.operation == "restore" ? RestoreData : Backup).new(run, token).call if token
  end
end
