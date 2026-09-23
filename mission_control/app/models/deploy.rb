# One run of houston deploy for a project, numbered per project. The process
# that started it holds its token; only that process may report progress or
# finish it, and only while it's still in flight. See the ownership template
# in docs/plans/deploy-path.md, Batch 3.
class Deploy < ApplicationRecord
  class Busy < StandardError
    attr_reader :deploy

    def initialize(deploy)
      @deploy = deploy
      super("deploy ##{deploy.number} is in flight (last heard from #{deploy.heartbeat_at.utc.iso8601})")
    end
  end

  class RestoreRefused < StandardError; end

  STATUSES = %w[queued in_flight go no_go].freeze
  # houston deploy reports at least every 30 s; after this long without a
  # word, the next deploy may take over.
  STALE_AFTER = 2.minutes
  LOG_CAP = 4.megabytes
  CHUNK_CAP = 256.kilobytes
  TRUNCATED = "\n[log truncated: Houston keeps the first 4 MiB of a deploy's log]\n".freeze

  belongs_to :project
  # A restore reads its snapshot from here.
  belongs_to :source_location, class_name: "StorageLocation", optional: true

  KINDS = %w[deploy restore].freeze
  validates :kind, inclusion: { in: KINDS }

  validates :sha, format: { with: /\A[0-9a-f]{40}\z/, message: "must be a full commit SHA (40 lowercase hex characters)" }
  validates :ref, presence: true, length: { maximum: 255 }
  validates :status, inclusion: { in: STATUSES }
  validates :step, length: { maximum: 100 }
  validates :error, length: { maximum: 1000 }

  scope :in_flight, -> { where(status: "in_flight") }
  # Everything but the log, which can be megabytes: for lists.
  scope :summary, -> { select(column_names - [ "log" ]) }

  # houston deploy's steps (internal/deploy), after houston runner's Test (step 00).
  STEPS = %w[Test Secrets Build Snapshot Accessories Release Deploy Post-deploy].freeze
  def short_sha = sha.first(7)

  # Each step as :done, :current, :failed, :pending, or :skipped (a hand
  # houston deploy runs no tests).
  def step_states
    return STEPS.index_with(:pending) if status == "queued"
    at = STEPS.index(step) || (runner ? 0 : 1)
    STEPS.each_with_index.to_h do |name, i|
      state = if name == "Test" && runner.nil? then :skipped
      elsif status == "go" || i < at then :done
      elsif i > at then :pending
      elsif status == "no_go" then :failed
      else :current
      end
      [ name, state ]
    end
  end

  def duration
    ((finished_at || Time.current) - created_at).to_i
  end

  def self.digest(token) = OpenSSL::Digest::SHA256.hexdigest(token)

  # Starts the project's next deploy. A silent in-flight deploy (no word for
  # STALE_AFTER) is finished as abandoned in the same transaction; a live
  # one raises Busy. Returns [deploy, token, number taken over or nil].
  def self.start!(project, sha:, ref:)
    token = SecureRandom.urlsafe_base64(32)
    transaction do
      took_over = nil
      if (current = project.deploys.in_flight.first)
        raise Busy, current if current.heartbeat_at > STALE_AFTER.ago
        took_over = current.abandon!
      end
      number = (project.deploys.maximum(:number) || 0) + 1
      deploy = project.deploys.create!(number:, sha:, ref:, token_digest: digest(token), heartbeat_at: Time.current, generation: project.data_generation)
      [ deploy, token, took_over ]
    end
  end

  # Queues a deploy of sha for a runner (build step 4). One queued deploy per
  # project: a newer push switches it, so a burst of pushes deploys once,
  # the newest. Runs in the caller's transaction when there is one.
  def self.queue!(project, sha:, ref:)
    transaction do
      if (queued = project.deploys.find_by(status: "queued"))
        queued.update!(sha:, ref:, log: queued.log + "A newer push switched to #{sha.first(7)} (#{ref}).\n")
        queued
      else
        number = (project.deploys.maximum(:number) || 0) + 1
        project.deploys.create!(number:, sha:, ref:, status: "queued", token_digest: "", heartbeat_at: Time.current)
      end
    end
  end

  # Queues a restore of project to a snapshot, code and data together, into
  # the next data generation (docs/plans/restore.md). Refused, with nothing
  # created, unless everything it needs is there, the snapshot's commit in
  # the repo included.
  def self.request_restore!(project, snapshot:, location:, confirm:)
    raise RestoreRefused, "type #{project.name} to confirm" unless confirm == project.name
    raise RestoreRefused, "link the repo first (houston link): a restore fetches the snapshot's commit from it" if project.repo_url.blank?
    if (busy = project.deploys.where(status: %w[queued in_flight]).order(:number).first)
      raise RestoreRefused, "#{busy.restore? ? "restore" : "deploy"} ##{busy.number} is #{busy.status.humanize(capitalize: false)}; wait for ##{busy.number}"
    end
    unless location && Snapshots.locations_for(project).include?(location)
      raise RestoreRefused, "#{project.name} never backed up to #{location&.name || "that location"}"
    end
    found = Snapshots.for(project, location).find { |s| snapshot.present? && (s.id == snapshot || s.short_id == snapshot) }
    raise RestoreRefused, "snapshot #{snapshot} isn't in #{location.name}'s snapshots of #{project.name}" unless found
    commit = GitRemote.commit(project, found.sha)
    raise RestoreRefused, "commit #{found.sha.first(7)} isn't in #{project.repo_url} any more (was history rewritten, or the repo relinked?): #{commit.error}" unless commit.ok

    transaction do
      number = (project.deploys.maximum(:number) || 0) + 1
      project.deploys.create!(number:, sha: found.sha, ref: "restore:#{found.short_id}", status: "queued", kind: "restore", token_digest: "",
                              heartbeat_at: Time.current, generation: project.data_generation + 1,
                              source_snapshot_id: found.id, source_location: location)
    end
  rescue Snapshots::Unavailable => e
    raise RestoreRefused, "can't read #{location.name}'s snapshots: #{e.message}"
  rescue ActiveRecord::RecordNotUnique
    # Two requests at once: the one-queued-per-project index took the other.
    raise RestoreRefused, "another deploy or restore of #{project.name} was just queued; wait for it"
  end

  # Hands the oldest claimable queued deploy to runner: [deploy, token,
  # the number it took over or nil], or nil. Silent in-flight deploys are
  # finished first; a project with a live one keeps its queued deploy back.
  def self.claim_next!(runner:)
    transaction do
      took_over = in_flight.where(heartbeat_at: ...STALE_AFTER.ago).to_h { |stale| [ stale.project_id, stale.abandon! ] }
      candidate = where(status: "queued").where.not(project_id: in_flight.select(:project_id)).order(:created_at, :id).first
      deploy, token = candidate && claim!(candidate, runner:)
      deploy && [ deploy, token, took_over[deploy.project_id] ]
    end
  end

  # Flips deploy from queued to in flight for runner, only if it's still
  # queued (another runner may have had it since it was picked). Returns
  # [deploy, token] or nil.
  def self.claim!(deploy, runner:)
    token = SecureRandom.urlsafe_base64(32)
    claimed = where(id: deploy.id, status: "queued").update_all(status: "in_flight", runner:, token_digest: digest(token),
                                                                 generation: deploy.project.data_generation,
                                                                 heartbeat_at: Time.current, updated_at: Time.current)
    claimed == 1 ? [ deploy.reload, token ] : nil
  end

  # Finishes a silent in-flight deploy; returns its number.
  def abandon!
    update!(status: "no_go", finished_at: Time.current, error: "abandoned: no word from houston deploy since #{heartbeat_at.utc.iso8601}")
    number
  end

  def owned_by?(token)
    token.present? && ActiveSupport::SecurityUtils.secure_compare(self.class.digest(token), token_digest)
  end

  def in_flight? = status == "in_flight"
  def restore? = kind == "restore"

  # Applies one progress report. The caller has checked ownership inside the
  # same transaction. The log is appended in SQL, so a 4 MiB log isn't read
  # back on every chunk. Returns what was appended to the log, or nil.
  def report!(step: nil, log: nil, status: nil, error: nil)
    self.step = step if step
    self.error = error if error
    if status
      self.status = status
      self.finished_at = Time.current
    end
    self.heartbeat_at = Time.current
    save!
    append_log(log) if log.present?
  end

  private
    def append_log(chunk)
      size = self.class.where(id:).pick(Arel.sql("length(CAST(log AS BLOB))")).to_i
      return nil if size >= LOG_CAP

      room = LOG_CAP - size
      piece = chunk.bytesize > room ? chunk.byteslice(0, room).scrub("") + TRUNCATED : chunk
      self.class.where(id:).update_all([ "log = log || ?", piece ])
      piece
    end
end
