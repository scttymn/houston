# One backup of a project: queued, run by BackupJob (which claims it with a
# token), then GO, NO-GO or skipped. The snapshot it makes lives in restic;
# this records the run. See docs/plans/volumes-backups.md, Batch 1.
class BackupRun < ApplicationRecord
  # The flight board shows each project's last backup. Status changes made
  # with update_all (a claim, a finish, giving up) refresh it by hand.
  after_commit -> { FlightBoard.refresh! }, if: -> { previously_new_record? || saved_change_to_status? }

  class Refused < StandardError; end

  NOTHING_DEPLOYED = "nothing deployed yet"

  STATUSES = %w[queued running go no_go skipped].freeze
  KINDS = %w[auto deploy restore].freeze
  # backup: a snapshot; restore: a restore deploy's data put back from one.
  OPERATIONS = %w[backup restore].freeze
  REASONS = %w[schedule manual deploy restore].freeze
  # BackupJob beats every 15 s; after this long without a word, the run is
  # abandoned and the project's next backup may start.
  STALE_AFTER = 2.minutes
  LOG_CAP = 1.megabyte

  belongs_to :project
  belongs_to :location, class_name: "StorageLocation"

  validates :status, inclusion: { in: STATUSES }
  validates :kind, inclusion: { in: KINDS }
  validates :reason, inclusion: { in: REASONS }
  validates :operation, inclusion: { in: OPERATIONS }

  scope :running, -> { where(status: "running") }

  def self.digest(token) = OpenSSL::Digest::SHA256.hexdigest(token)

  # Queues a backup of project to the default location and enqueues its job.
  # A manual one already queued is returned as it is (a double click).
  # scheduled_for: the local date a scheduled backup is for; that day's run
  # (whatever its status) is returned instead of a second one.
  # deploy_number: a pre-deploy snapshot (kind deploy), one per deploy, or,
  # with reason restore, a restore's safety snapshot, one per restore.
  def self.request!(project, reason: "manual", scheduled_for: nil, deploy_number: nil)
    raise Refused, NOTHING_DEPLOYED unless project.running_deploy
    location = project.backup_location
    raise Refused, "no backup storage yet (finish setup's storage step)" unless location

    same = if deploy_number then { operation: "backup", reason:, deploy_number: }
    elsif scheduled_for then { scheduled_for: }
    else { status: "queued", reason: }
    end
    run = transaction do
      project.backup_runs.find_by(same) ||
        project.backup_runs.create!(location:, kind: deploy_number ? "deploy" : "auto", reason:, scheduled_for:, deploy_number:,
                                    status: "queued", heartbeat_at: Time.current)
    end
    BackupJob.perform_later(run) if run.previously_new_record?
    run
  rescue ActiveRecord::RecordNotUnique
    project.backup_runs.find_by!(same)
  end

  # Queues the data restore of a restore deploy: its snapshot, from its
  # location, into the generation it builds. One per restore deploy; a retry
  # gets the same run.
  def self.request_restore!(deploy)
    project = deploy.project
    run = transaction do
      project.backup_runs.find_by(operation: "restore", deploy_number: deploy.number) ||
        project.backup_runs.create!(operation: "restore", kind: "restore", reason: "restore", deploy_number: deploy.number,
                                    source_snapshot_id: deploy.source_snapshot_id, location: deploy.source_location,
                                    status: "queued", heartbeat_at: Time.current)
    end
    BackupJob.perform_later(run) if run.previously_new_record?
    run
  rescue ActiveRecord::RecordNotUnique
    project.backup_runs.find_by!(operation: "restore", deploy_number: deploy.number)
  end

  # Flips run from queued to running with a new token, only if it's still
  # queued and the project has no live running backup (a silent one is
  # abandoned first). Returns the token, :busy, or nil (not queued).
  def self.claim!(run)
    token = SecureRandom.urlsafe_base64(32)
    transaction do
      run.project.backup_runs.running.where(heartbeat_at: ...STALE_AFTER.ago).find_each(&:abandon!)
      return :busy if run.project.backup_runs.running.where.not(id: run.id).exists?
      # While a restore is queued or in flight, only its own runs (its safety
      # snapshot, its data) go ahead: a backup now would catch it half done.
      return :busy if run.reason != "restore" && run.project.deploys.where(kind: "restore", status: %w[queued in_flight]).exists?

      now = Time.current
      claimed = where(id: run.id, status: "queued").update_all(status: "running", token_digest: digest(token), heartbeat_at: now,
                                                             started_at: now, updated_at: now)
      FlightBoard.refresh! if claimed == 1
      claimed == 1 ? token : nil
    end
  rescue ActiveRecord::RecordNotUnique
    :busy
  end

  def abandon!
    update!(status: "no_go", finished_at: Time.current, error: stale_error)
  end

  # A queued run that will never start (its job gave up waiting).
  def give_up!(reason)
    gave_up = self.class.where(id:, status: "queued").update_all(status: "no_go", error: reason, finished_at: Time.current, updated_at: Time.current)
    FlightBoard.refresh! if gave_up == 1
    gave_up
  end

  # Running, but silent for STALE_AFTER: Mission Control stopped during it.
  # The next claim for the project records that; the page says so meanwhile.
  def stale? = status == "running" && heartbeat_at < STALE_AFTER.ago

  def stale_error = "Mission Control stopped during the backup (no word since #{heartbeat_at.utc.iso8601})"

  # Writes below go through ours: only the job holding token may write, and
  # only while the run is still running. A run that was abandoned and taken
  # over stays as it is. Each returns whether it wrote.
  def heartbeat!(token) = ours(token).update_all(heartbeat_at: Time.current) == 1

  # Records sha, the version being backed up.
  def begin!(token, sha:) = ours(token).update_all(sha:, updated_at: Time.current) == 1

  # Finishes the run; false when it isn't ours any more (nothing written).
  def finish!(token, status:, error: nil, snapshot_id: nil, bytes: nil, found: {}, log: "")
    log = log.to_s
    log = log.byteslice(0, LOG_CAP).scrub + "\n[log truncated at 1 MiB]\n" if log.bytesize > LOG_CAP
    written = ours(token).update_all(status:, error: error&.truncate(4000), snapshot_id:, bytes:, found:, log:,
                                     finished_at: Time.current, updated_at: Time.current)
    reload
    FlightBoard.refresh! if written == 1
    written == 1
  end

  private
    def ours(token) = self.class.where(id:, status: "running", token_digest: self.class.digest(token.to_s))
end
