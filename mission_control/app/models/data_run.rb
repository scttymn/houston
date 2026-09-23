# What a backup and a restore's data engine share: a run claimed with a token
# (BackupRun), docker commands under one 3-hour deadline (a timeout, exit 124,
# stops the run), a heartbeat from a thread, cleanup that never raises, and a
# finish that writes only while the run is still this job's. Subclasses name
# their staging volume and helper containers, and do the work in #steps.
class DataRun
  DEADLINE = 3.hours
  class_attribute :heartbeat_every, default: 15.seconds

  WRITE = 'mkdir -p "$(dirname "$1")" && cat > "$1"'

  class Failed < StandardError; end
  class TimedOut < StandardError; end

  def initialize(run, token)
    @run = run
    @token = token
    @project = run.project
    @location = run.location
    @warnings = []
    @log = +""
  end

  private
    # Runs the subclass's steps under the deadline and the heartbeat: every
    # path finishes the run and removes the staging volume.
    def run_steps(what)
      @started = Process.clock_gettime(Process::CLOCK_MONOTONIC)
      with_heartbeat do
        yield
      rescue Failed => e
        finish("no_go", error: e.message)
      rescue TimedOut
        clean_up("rm", "-f", *containers)
        finish("no_go", error: "took longer than #{DEADLINE.inspect}; stopped")
      rescue StandardError => e
        # A bug or a surprise: the run still finishes (the job then fails loudly).
        finish("no_go", error: "Houston failed during the #{what}: #{e.class}: #{e.message}")
        raise
      ensure
        clean_up("volume", "rm", "-f", staging)
      end
    end

    def prepare
      docker("rm", "-f", *containers)
      docker("volume", "rm", "-f", staging)
      created = docker("volume", "create", staging)
      raise Failed, "couldn't create the staging volume: #{tail(created.output)}" unless created.success
    end

    def finish(status, **fields)
      Rails.logger.warn("#{self.class.name.underscore.humanize.downcase} of #{@project.name}: run #{@run.id} was taken over; not finishing it") unless
        @run.finish!(@token, status:, log: @log, **fields)
    end

    # Each command gets what's left of the deadline; a timeout (exit 124)
    # stops the whole run.
    def docker(*args, env: {}, stdin: nil)
      @log << "$ docker #{args.first(4).join(" ")} …\n"
      result = DockerCommand.run(*args, env:, stdin:, timeout: remaining)
      raise TimedOut if result.code == 124
      result
    end

    # Cleanup runs even after the deadline, briefly, and never raises.
    def clean_up(*args) = DockerCommand.run(*args, timeout: 60)

    def pipe(from, to)
      @log << "$ docker #{from.first(3).join(" ")} … | docker … #{to.last}\n"
      result = DockerCommand.pipe(from, to, timeout: remaining)
      raise TimedOut if result.code == 124
      result
    end

    def remaining
      left = (DEADLINE - (Process.clock_gettime(Process::CLOCK_MONOTONIC) - @started)).to_i
      raise TimedOut if left < 1
      left
    end

    # A libpq connection string naming the database, so a name with "=" or
    # a URI prefix can't be read as connection options.
    def conninfo(name) = "dbname='#{name.b.gsub(/[\\']/) { "\\#{$&}" }}'"

    def tools = ENV.fetch("HOUSTON_TOOLS_IMAGE", "houston/mission-control:local")

    def tail(output) = output.to_s.lines.reject { |l| l.start_with?('{"message_type":"status"') }.last(20).join.strip.truncate(2000)

    # The run's heartbeat, from a thread, while the run works.
    def with_heartbeat
      beat = Thread.new do
        loop do
          sleep heartbeat_every
          ActiveRecord::Base.connection_pool.with_connection { @run.heartbeat!(@token) }
        end
      end
      yield
    ensure
      beat&.kill&.join
    end
end
