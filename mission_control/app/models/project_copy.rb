# Copying a project to a new name (docs/plans/copy-project.md). A push whose
# compose.yml names a new project holds; the admin asks, and the new project
# is made from the branch head with the old one's secrets, volume placement,
# backup target and repo link (its webhook secret too, so the git host's
# webhook keeps working). Its first deploy, kind copy, builds it, copies the
# old one's data from a late snapshot, boots it, and takes over the hosts
# the two share. The old project is untouched otherwise, until it's deleted.
class ProjectCopy < ApplicationRecord
  class Refused < StandardError; end

  STATUSES = %w[queued running go no_go].freeze
  ACTIVE = %w[queued running].freeze

  belongs_to :from_project, class_name: "Project", optional: true
  belongs_to :project, optional: true
  belongs_to :deploy, optional: true
  belongs_to :snapshot_run, class_name: "BackupRun", optional: true

  validates :status, inclusion: { in: STATUSES }

  scope :active, -> { where(status: ACTIVE) }

  def self.request!(from, confirm:, by:)
    raise Refused, "type #{from.name} to confirm" unless confirm == from.name
    if from.copies_from.active.exists?
      raise Refused, "#{from.name} is already being copied to #{from.copies_from.active.first.to}"
    end
    if (busy = from.deploys.where(status: %w[queued in_flight]).order(:number).first)
      raise Refused, "#{busy.restore? ? "restore" : "deploy"} ##{busy.number} is #{busy.status.humanize(capitalize: false)}; wait for ##{busy.number}"
    end
    if (run = from.backup_runs.where(status: %w[queued running]).order(:id).first)
      raise Refused, "#{run.operation == "restore" ? "a restore's data" : "a backup"} of #{from.name} is #{run.status}; wait for it"
    end
    raise Refused, "#{from.name} is being deleted" if from.deleting?
    proposal = from.copy_proposal or raise Refused, "#{from.name}'s latest deploy doesn't propose a copy: change name: in compose.yml, push, and its deploy holds with the new name"
    to = proposal.proposed_name
    if (refusal = Project.name_refusal(to))
      raise Refused, refusal
    end
    if (from.volumes.any? || from.databases.any?) && from.running_deploy && !from.backup_location
      raise Refused, "no backup storage for the copy's snapshot of #{from.name}'s data (finish setup's storage step)"
    end

    read = GitRemote.read(from)
    raise Refused, "couldn't read #{from.repo_url}: #{read.problems}" unless read.ok
    sync = read.inspection["sync"].to_h
    unless sync["name"] == to
      raise Refused, "#{from.name}'s compose.yml on #{from.branch} now names #{sync["name"]}, not #{to}: wait for its deploy to hold, then copy"
    end
    check = ProjectSync.new(sync)
    raise Refused, check.errors.map { |field, messages| "#{field}: #{messages.to_sentence}" }.to_sentence unless check.valid?

    copy = transaction do
      project = check.save!(link: from.slice(:repo_url, :branch, :compose_path, :deploy_key_private, :deploy_key_public, :webhook_secret).symbolize_keys)
      raise Refused, "#{to} is another project" unless project.previously_new_record? || project.deploys.none?
      project.update!(webhook_verified_at: from.webhook_verified_at, seen_refs: from.seen_refs, chosen_backup_location: from.chosen_backup_location)
      referenced = project.variables.map { |v| v["name"] }
      from.secrets.where(key: referenced).each { |secret| project.secrets.create!(key: secret.key, value: secret.value) }
      from.project_volumes.where(name: project.volumes.map { |v| v["name"] }).each { |v| project.project_volumes.create!(name: v.name, location: v.location) }
      deploy = project.deploys.create!(number: 1, sha: read.sha, ref: "refs/heads/#{from.branch}", status: "queued", kind: "copy",
                                       token_digest: "", heartbeat_at: Time.current, sync_payload: sync)
      from.copies_from.create!(project:, deploy:, from: from.name, to:, sha: read.sha, by:)
    end
    FlightBoard.refresh!
    copy
  rescue ProjectSync::Refused => e
    raise Refused, e.message
  rescue ActiveRecord::RecordNotUnique
    raise Refused, "#{from.name} is already being copied"
  end

  # Before the handover: the copy's deploy ends, cancelled, and the new
  # project goes (Deploy#settle_copy). The lock is the handover's too, so a
  # cancel and a handover never cross.
  def cancel!(by:)
    with_lock do
      raise Refused, "the copy to #{to} is done" unless status.in?(ACTIVE)
      raise Refused, "#{to} has taken over #{from}'s hosts; undo the copy instead" if handed_over_at
      deploy.update!(status: "no_go", error: "cancelled by #{by}", finished_at: Time.current)
    end
  end

  # After a GO copy, while the old project is there: the shared hosts go
  # back to it, then the new project is deleted (its backups kept, with a
  # final snapshot, as any delete).
  def undo!(confirm:, by:)
    raise Refused, "type #{to} to confirm" unless confirm == to
    raise Refused, "the copy to #{to} isn't done" unless status == "go"
    raise Refused, "#{from} is deleted: there's nothing to go back to" unless from_project
    raise Refused, "#{to} is gone already" unless project

    begin
      Handover.new(self).back!
    rescue Handover::Failed => e
      raise Refused, "the hosts didn't go back to #{from}: #{e.message}"
    end
    deletion = begin
      ProjectDeletion.request!(project, confirm: project.name, delete_backups: false, by:)
    rescue ProjectDeletion::Refused => e
      raise Refused, "the hosts are #{from}'s again, but #{to} wasn't deleted: #{e.message}"
    end
    update!(undone_at: Time.current)
    deletion
  end
end
