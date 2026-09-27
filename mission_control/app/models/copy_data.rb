# A copy's data (docs/plans/copy-project.md, Batch 4): a snapshot of the old
# project, taken when the copy's runner asks (as late as possible: after the
# build, with the new project's accessories up), then restored into the new
# project's generation 1 by RestoreData. Both are BackupRuns, so they're
# claimed, heartbeat and finish like any backup or restore. The runner polls
# status, which moves the copy from one to the other.
module CopyData
  # The snapshot, asked for once; a retried POST gets the same run.
  def self.start!(deploy)
    copy = deploy.copy
    old = copy.from_project
    return skipped("#{copy.from} is gone: its data can't be copied") unless old
    return skipped("#{copy.from} has no data to copy") if old.volumes.empty? && old.databases.empty?
    return skipped("#{copy.from} has never been deployed: no data to copy") unless old.running_deploy

    copy.with_lock do
      unless copy.snapshot_run
        location = old.backup_location or return { id: 0, status: "no_go", error: "no backup storage for the copy's snapshot" }
        run = old.backup_runs.create!(location:, kind: "deploy", reason: "copy", status: "queued", heartbeat_at: Time.current)
        copy.update!(snapshot_run: run)
        BackupJob.perform_later(run)
      end
    end
    status(deploy)
  end

  # Where the data stands: the snapshot while it runs, then the restore.
  def self.status(deploy)
    copy = deploy.copy
    snapshot = copy.snapshot_run
    unless snapshot
      old = copy.from_project
      return skipped("#{copy.from} has no data to copy") if old.nil? || (old.volumes.empty? && old.databases.empty?) || !old.running_deploy
      return { id: 0, status: "queued", error: nil }
    end

    case snapshot.status
    when "queued", "running" then RemoteView.backup(snapshot)
    when "skipped" then skipped(snapshot.error)
    when "no_go" then RemoteView.backup(snapshot).merge(error: "the snapshot of #{copy.from} failed: #{snapshot.error}")
    else
      deploy.update!(source_snapshot_id: snapshot.snapshot_id, source_location: snapshot.location) unless deploy.source_snapshot_id
      RemoteView.backup(BackupRun.request_restore!(deploy))
    end
  end

  def self.skipped(why) = { id: 0, status: "skipped", error: why }
  private_class_method :skipped
end
