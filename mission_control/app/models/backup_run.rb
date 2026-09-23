# One backup of a project: queued, run by BackupJob (which claims it with a
# token), then GO, NO-GO or skipped. The snapshot it makes lives in restic;
# this records the run. See docs/plans/volumes-backups.md, Batch 1.
class BackupRun < ApplicationRecord
  class Refused < StandardError; end

  STATUSES = %w[queued running go no_go skipped].freeze
  KINDS = %w[auto deploy].freeze
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

  scope :running, -> { where(status: "running") }

  def self.digest(token) = OpenSSL::Digest::SHA256.hexdigest(token)

  # Queues a backup of project to the default location and enqueues its job.
  # A manual one already queued is returned as it is (a double click).
  def self.request!(project, reason: "manual")
    raise Refused, "nothing deployed yet" unless project.running_deploy
    location = project.backup_location
    raise Refused, "no backup storage yet (finish setup's storage step)" unless location

    run = transaction do
      project.backup_runs.find_by(status: "queued", reason:) ||
        project.backup_runs.create!(location:, kind: "auto", reason:, status: "queued", heartbeat_at: Time.current)
    end
    BackupJob.perform_later(run) if run.previously_new_record?
    run
  rescue ActiveRecord::RecordNotUnique
    project.backup_runs.find_by!(status: "queued", reason:)
  end

  # Flips run from queued to running with a new token, only if it's still
  # queued and the project has no live running backup (a silent one is
  # abandoned first). Returns the token, :busy, or nil (not queued).
  def self.claim!(run)
    token = SecureRandom.urlsafe_base64(32)
    transaction do
      run.project.backup_runs.running.where(heartbeat_at: ...STALE_AFTER.ago).find_each(&:abandon!)
      return :busy if run.project.backup_runs.running.where.not(id: run.id).exists?

      now = Time.current
      claimed = where(id: run.id, status: "queued").update_all(status: "running", token_digest: digest(token), heartbeat_at: now,
                                                             started_at: now, updated_at: now)
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
    self.class.where(id:, status: "queued").update_all(status: "no_go", error: reason, finished_at: Time.current, updated_at: Time.current)
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
    written == 1
  end

  private
    def ours(token) = self.class.where(id:, status: "running", token_digest: self.class.digest(token.to_s))
end
