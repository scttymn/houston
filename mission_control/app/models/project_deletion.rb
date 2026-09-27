# Deleting a project (docs/plans/delete-project.md): the request, the run
# (DeleteProjectJob, through ProjectRemoval), and afterwards the record of
# it. The project's name, repo and final snapshot outlive its row.
#
# Until removing_at is set (the final snapshot taken), a failure cancels it
# and the project serves as before. After that, the project is deleting
# until the deletion finishes GO: nothing new starts for it, and asking
# again resumes the same deletion.
class ProjectDeletion < ApplicationRecord
  class Refused < StandardError; end

  STATUSES = %w[queued running go no_go].freeze
  ACTIVE = %w[queued running].freeze
  # The job beats every 15 s; after this long without a word, asking again
  # takes the deletion over.
  STALE_AFTER = 2.minutes
  LOG_CAP = 1.megabyte

  belongs_to :project, optional: true
  belongs_to :snapshot_location, class_name: "StorageLocation", optional: true

  validates :status, inclusion: { in: STATUSES }

  after_commit -> { FlightBoard.refresh! }, if: -> { previously_new_record? || saved_change_to_status? || saved_change_to_step? }

  # Deletions that keep their project from starting anything new.
  scope :holding, -> { where(status: ACTIVE).or(where(status: "no_go").where.not(removing_at: nil)) }
  # Past the final snapshot and not finished: its project is being taken apart.
  scope :removing, -> { where.not(removing_at: nil).where.not(status: "go").where.not(project_id: nil) }

  # Queues the deletion of project once the admin typed its name; refused,
  # with nothing written, while anything else runs for it. One stopped
  # during removal is resumed as it was asked (its backups choice included).
  def self.request!(project, confirm:, delete_backups:, by:)
    raise Refused, "type #{project.name} to confirm" unless confirm == project.name

    deletion = transaction do
      if (busy = project.deploys.where(status: %w[queued in_flight]).order(:number).first)
        raise Refused, "#{busy.restore? ? "restore" : "deploy"} ##{busy.number} is #{busy.status.humanize(capitalize: false)}; wait for ##{busy.number}"
      end
      if (run = project.backup_runs.where(status: %w[queued running]).order(:id).first)
        raise Refused, "#{run.operation == "restore" ? "a restore's data" : "a backup"} of #{project.name} is #{run.status}; wait for it"
      end

      current = project.deletions.holding.order(:id).last
      if current&.status.in?(ACTIVE) && !current.stale?
        raise Refused, "#{project.name} is already being deleted"
      elsif current
        current.update!(status: "queued", error: nil, finished_at: nil, heartbeat_at: Time.current)
        current
      else
        project.deletions.create!(name: project.name, repo_url: project.repo_url, by:, delete_backups:, heartbeat_at: Time.current)
      end
    end
    DeleteProjectJob.perform_later(deletion)
    deletion
  rescue ActiveRecord::RecordNotUnique
    raise Refused, "#{project.name} is already being deleted"
  end

  STEP_WORDS = {
    "check" => "Check Docker, Cloudflare and storage",
    "snapshot" => "Final snapshot",
    "routes" => "Maintenance routes",
    "dns" => "DNS records",
    "containers" => "Containers and kamal-proxy's route",
    "volumes" => "Volumes",
    "folders" => "Volume folders on storage",
    "images" => "Images",
    "registry" => "Images in the registry",
    "files" => "Kamal's files and the runners' checkouts",
    "backups" => "Backups",
    "rows" => "Mission Control's records"
  }.freeze

  # Each step with its state, as a deploy page shows them: done before the
  # current step, then current (running), failed (NO-GO) or pending.
  def steps
    at = STEP_WORDS.keys.index(step)
    STEP_WORDS.each_with_index.map do |(name, words), i|
      state = if status == "go" then :done
      elsif at.nil? || i > at then :pending
      elsif i < at then :done
      else { "running" => :current, "no_go" => :failed }.fetch(status, :pending)
      end
      [ words, state ]
    end
  end

  def active? = status.in?(ACTIVE)

  # Stopped before anything was removed: the project serves as before.
  def cancelled? = status == "no_go" && !removing?

  def stale? = status == "running" && heartbeat_at < STALE_AFTER.ago

  def removing? = removing_at.present?
end
