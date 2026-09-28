# One download of a snapshot: everything it holds (the volumes' files under
# data/, the databases and houston.json under out/) as a zip, streamed out
# of restic as it's read (docs/plans/download-snapshot.md). It's the Rack
# body: each yields the zip, close cleans up.
#
# One download runs at a time, server-wide: the restic container's name is
# the lock, and Docker takes it atomically. Only the project's own listed
# snapshots can be downloaded; repositories are shared between projects.
class SnapshotExport
  class NotFound < StandardError; end
  class Busy < StandardError; end
  class Failed < StandardError; end

  CONTAINER = "houston-export"
  # Each download's container is labelled with its own token, so cleanup
  # removes only its own: by then the name may belong to the next download.
  LABEL = "houston.export"
  DEADLINE = 3.hours
  # A container older than this outlived its deadline: nothing will stop it.
  STALE_AFTER = DEADLINE + 10.minutes
  ZIP = "PK\x03\x04".b

  def self.open(project, location_name:, snapshot:, by:)
    location = Snapshots.locations_for(project).find { |l| l.name == location_name }
    raise NotFound, "#{project.name} never backed up to #{location_name.presence || "that location"}" unless location

    found = Snapshots.for(project, location).find { |s| snapshot.present? && (s.id == snapshot || s.short_id == snapshot) }
    raise NotFound, "snapshot #{snapshot} isn't in #{location.name}'s snapshots of #{project.name}" unless found

    new(project, location, found, by).tap(&:start)
  rescue Snapshots::Unavailable => e
    raise Failed, "can't read #{location.name}'s snapshots: #{e.message}"
  end

  attr_reader :filename

  def initialize(project, location, snapshot, by)
    @project, @location, @snapshot, @by = project, location, snapshot, by
    @filename = "#{project.name}-#{snapshot.time.utc.strftime("%Y%m%d-%H%M")}Z-#{snapshot.short_id}.zip"
    @token = SecureRandom.hex(16)
    @bytes = 0
    @ended = false
  end

  def start
    @download = dump
    @download = dump if in_use?(@download) && stale_removed?
    refuse(@download) if @download.is_a?(DockerCommand::Result)

    unless @download.first.start_with?(ZIP)
      stop
      raise Failed, "restic didn't send a zip: #{@download.first.first(200).scrub.strip}"
    end
    log "started"
  end

  # The response's headers: a zip to save, never cached, never buffered by a
  # proxy. (Nothing in the stack reads it whole: Rack::ETag digests only a
  # body with to_ary, and this one has none.)
  def headers
    { "Content-Type" => "application/zip", "Content-Disposition" => ActionDispatch::Http::ContentDisposition.format(disposition: "attachment", filename:),
      "Cache-Control" => "no-store", "X-Accel-Buffering" => "no" }
  end

  def each
    @download.each do |chunk|
      @bytes += chunk.bytesize
      yield chunk
    end
    @ended = true
    log "finished, #{@bytes} bytes"
  rescue DockerCommand::Broken => e
    @ended = true
    log "broke after #{@bytes} bytes: #{e.message}"
    raise
  end

  def close
    return if @ended

    stop
    log "stopped by the client after #{@bytes} bytes"
  end

  private
    def dump
      DockerCommand.download(*@location.restic_args("dump", "--quiet", "--retry-lock", "30s", "--archive", "zip", @snapshot.id, "/", name: CONTAINER, labels: { LABEL => @token }),
                             env: @location.restic_env, timeout: DEADLINE.to_i)
    end

    def in_use?(ran) = ran.is_a?(DockerCommand::Result) && ran.code == 125 && ran.output.include?("is already in use")

    # The container holding the name, if it started too long ago to be alive
    # and well, is removed (true), by its id: by name, a download that took
    # the name since would be hit. None there: the name is free (true).
    def stale_removed?
      holder = self.holder
      return true unless holder
      return false if holder[:started] > STALE_AFTER.ago

      DockerCommand.run("rm", "-f", holder[:id])
      true
    end

    # The container holding the name now: {id, started, token}, or nil.
    def holder
      inspected = DockerCommand.run("inspect", "-f", "{{.Id}} {{.Created}} {{index .Config.Labels \"#{LABEL}\"}}", CONTAINER)
      return unless inspected.success

      id, created, token = inspected.output.strip.split(" ", 3)
      { id:, started: Time.iso8601(created), token: }
    rescue ArgumentError, TypeError
      nil
    end

    def refuse(ran)
      if in_use?(ran)
        started = holder&.dig(:started)
        raise Busy, "another download#{" started at #{started.in_time_zone.strftime("%H:%M %Z")}" if started} is still running; try again when it's done"
      end
      raise Failed, "the repository is busy (pruning?); try again in a few minutes" if ran.code == 11
      raise Failed, "restic couldn't read the snapshot (exit #{ran.code}): #{ran.output.to_s.lines.last(5).join.strip.truncate(500)}"
    end

    # Stops the docker CLI, then removes this download's container if it's
    # still there (stopping the CLI doesn't stop the container).
    def stop
      @download.close
      own = holder
      DockerCommand.run("rm", "-f", own[:id]) if own && own[:token] == @token
    end

    def log(what)
      Rails.logger.info "download: #{@by} #{@project.name} #{@snapshot.short_id} from #{@location.name}: #{what}"
    end
end
