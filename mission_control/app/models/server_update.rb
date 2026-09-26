# Updates this server to a Houston release, started from Mission Control
# (docs/plans/update-from-mission-control.md), so it doesn't take SSH on the
# server's network. The helper container houston-update runs the release's
# installer on the host (lib/update-helper.sh) and, if that fails, installs
# the version the server ran again. ServerUpdateJob records how it went.
#
# One runs at a time: the running row is the lock (a partial unique index).
# While it runs, runners get no deploys and backups wait: the installer
# recreates the runners, which would cut one short.
class ServerUpdate < ApplicationRecord
  class Refused < StandardError; end

  HELPER = "houston-update"
  SCRIPT = Rails.root.join("lib/update-helper.sh")
  STATUSES = %w[running go rolled_back no_go].freeze
  # Past this, the board says it's taking long and each check logs it. The
  # helper gives each install 20 minutes, so it ends within about 40.
  LONG = 20.minutes
  # How long the board says how the last one went.
  SHOWN_FOR = 1.day
  # The helper's log kept: plenty for an installer run, bounded.
  LOG_LINES = 500

  validates :status, inclusion: { in: STATUSES }

  scope :running, -> { where(status: "running") }

  def self.running? = running.exists?

  # What the board shows: the running update, or the last one to finish
  # within SHOWN_FOR.
  def self.shown = running.first || where(finished_at: SHOWN_FOR.ago..).order(:finished_at).last

  def long? = status == "running" && started_at < LONG.ago

  # The installer's steps so far (its "==> " lines), each with its state as
  # a deploy page shows them: the last one is current while it runs, and the
  # one that broke is failed. After a rollback that's the step before the
  # helper put the old version back; the old version's own steps are done.
  def steps
    names = log.to_s.lines.filter_map { |line| self.class.step_name(line) if line.start_with?("==> ") }
    states = Array.new(names.size, :done)
    back = names.index { it.include?("didn't install; putting") }
    states[back - 1] = :failed if back&.positive?
    case status
    when "running" then states[-1] = :current if names.any?
    when "no_go" then states[-1] = :failed if names.any?
    end
    names.zip(states)
  end

  def self.step_name(line) = line.delete_prefix("==> ").delete_prefix("houston update: ").strip.sub(/\A[a-z](?!\d)/, &:upcase).truncate(120)

  # Starts the update to version (default: the latest release known).
  # Refused, with nothing saved and no helper, when it can't run now.
  def self.start!(version = nil)
    from = HoustonVersion.current
    raise Refused, "this server runs #{from}, not a release, so it updates from where it was built" unless UpdateNotice.release?(from)
    to = version.presence || Installation.current.latest_release
    raise Refused, "no newer release is known yet" if to.blank?
    raise Refused, "#{to.truncate(40)} isn't a release (vX.Y.Z)" unless UpdateNotice.release?(to)
    unless UpdateNotice.newer(from, to)
      raise Refused, "this server already runs the latest release (#{from})" if version.blank?
      raise Refused, "#{to} isn't newer than #{from}, which this server runs"
    end
    if (current = running.first)
      raise Refused, "the update to #{current.to_version} is already running"
    end
    dir = OwnContainer.labels[OwnContainer::WORKING_DIR]
    raise Refused, "can't update from here: this Mission Control wasn't started by Houston's installer" if dir.blank?
    image = ENV["HOUSTON_RUNNER_IMAGE"].presence or
      raise Refused, "can't update from here yet: run the installer once more (it tells Mission Control the runner image to do it with)"

    update = begin
      create!(to_version: to, from_version: from, status: "running", started_at: Time.current)
    rescue ActiveRecord::RecordNotUnique
      raise Refused, "another update was just started"
    end
    begin
      # Saved first, then checked: once the row is saved no deploy or backup
      # can be claimed, so one claimed before it is caught here.
      busy!
      DockerCommand.run("rm", HELPER, timeout: 10) # a finished one; a running one stays, and the run below is refused
      result = DockerCommand.run("run", "-d", "--name", HELPER, "--privileged", "--pid=host", "--user", "0",
        "-e", "HOUSTON_UPDATE_TO=#{to}", "-e", "HOUSTON_UPDATE_FROM=#{from}", "-e", "HOUSTON_REPO=#{LatestRelease.repo}",
        "-e", "HOUSTON_DIR=#{dir}", "-e", "HOUSTON_RUNNERS=#{ENV["HOUSTON_RUNNERS"]}",
        "--entrypoint", "sh", image, "-c", SCRIPT.read, timeout: 30)
      raise Refused, "couldn't start the update: #{result.output.strip.lines.last&.strip}" unless result.success
    rescue Refused
      update.destroy!
      raise
    end
    Rails.logger.info("houston: updating to #{to} from #{from} (#{HELPER})")
    ServerUpdateJob.set(wait: ServerUpdateJob::FOLLOW_EVERY).perform_later(true)
    FlightBoard.refresh!
    update
  end

  def self.busy!
    if (deploy = Deploy.where(status: %w[queued in_flight]).order(:id).first)
      raise Refused, "#{deploy.restore? ? "restore" : "deploy"} ##{deploy.number} of #{deploy.project.name} is #{deploy.status.humanize(capitalize: false)}; update when it's done"
    end
    if (run = BackupRun.where(status: %w[queued running]).order(:id).first)
      raise Refused, "#{run.operation == "restore" ? "a restore" : "a backup"} of #{run.project.name} is #{run.status}; update when it's done"
    end
  end
  private_class_method :busy!

  # Once the helper has ended, records how it went, once (ServerUpdateJob).
  # Docker not answering changes nothing: the next check tries again.
  def self.settle!
    update = running.first or return
    state = DockerCommand.run("inspect", "--format", "{{.State.Status}} {{.State.ExitCode}}", HELPER, timeout: 10)
    if state.success
      status, code = state.output.split
      unless status.in?(%w[exited dead])
        follow(update)
        Rails.logger.warn("houston: the update to #{update.to_version} has run for #{((Time.current - update.started_at) / 60).floor} minutes; see docker logs #{HELPER} on the server") if update.long?
        return
      end
      log = DockerCommand.run("logs", "--tail", LOG_LINES.to_s, HELPER, timeout: 10).output
      result, log = outcome(update, code.to_i, log)
    elsif state.output.match?(/no such (object|container)/i)
      result, log = "no_go", "The update helper (#{HELPER}) is gone, so how the update went is unknown."
    else
      Rails.logger.warn("houston: couldn't check the update to #{update.to_version}: #{state.output.strip}")
      return
    end

    written = running.where(id: update.id).update_all(status: result, log:, finished_at: Time.current, updated_at: Time.current)
    return unless written == 1
    Rails.logger.public_send(result == "go" ? :info : :warn, "houston: the update to #{update.to_version}: #{result}")
    FlightBoard.refresh!
  end

  # Saves what the running update is doing: its log so far (Houston's page
  # shows it) and the installer's latest step (a "==> " line); the board
  # refreshes when the step changes.
  def self.follow(update)
    result = DockerCommand.run("logs", "--tail", LOG_LINES.to_s, HELPER, timeout: 10)
    return unless result.success
    line = result.output.lines.reverse.find { it.start_with?("==> ") }
    step = line ? step_name(line) : update.step
    moved = step != update.step
    return unless moved || result.output != update.log
    update.update_columns(step:, log: result.output, updated_at: Time.current)
    FlightBoard.refresh! if moved
  end
  private_class_method :follow

  # The helper exits 0 when it installed the new version, and 3 when it
  # failed and put the old one back.
  def self.outcome(update, code, log)
    case code
    when 0
      return [ "go", log ] if HoustonVersion.current == update.to_version
      [ "no_go", "#{log}The installer finished, but Mission Control runs #{HoustonVersion.current}.\n" ]
    when 3 then [ "rolled_back", log ]
    else [ "no_go", log ]
    end
  end
  private_class_method :outcome
end
