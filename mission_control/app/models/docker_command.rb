require "open3"

# Runs the docker CLI. Secrets are passed through the environment of the
# docker process (`docker run -e NAME` without a value), never in argv.
# Tests swap the runner for a recording fake.
class DockerCommand
  # code: the exit status (124 when a timeout stopped it).
  Result = Data.define(:success, :output, :code) do
    def initialize(success:, output:, code: success ? 0 : 1) = super
  end

  # A download's command failed after its first bytes were sent.
  class Broken < StandardError; end

  # A running command whose stdout is a file being sent (a snapshot's zip),
  # as a Rack body: first is its first bytes; each yields them and the rest,
  # then raises Broken if the command failed; close stops it if it's still
  # running.
  class Download
    CHUNK = 64.kilobytes
    STOP_WAIT = 5

    attr_reader :first

    def initialize(first, stdout, errors, wait)
      @first, @stdout, @errors, @wait = first, stdout, errors, wait
    end

    def each
      yield @first
      while (chunk = read)
        yield chunk
      end
      status = @wait.value
      raise Broken, "#{status.exitstatus ? "exit #{status.exitstatus}" : "stopped by signal #{status.termsig}"}: #{@errors.value.to_s.lines.last(5).join.strip}" unless status.success?
    end

    # Stops the command and everything it started (its process group): TERM,
    # then KILL after STOP_WAIT. It never waits longer, so a hung docker
    # can't hold the web server's thread.
    def close
      signal("TERM") if @wait.alive?
      @stdout.close unless @stdout.closed?
      unless @wait.join(STOP_WAIT)
        signal("KILL")
        @wait.join(STOP_WAIT)
      end
      @errors.join(STOP_WAIT)
    end

    private
      def signal(name)
        Process.kill(name, -@wait.pid)
      rescue Errno::ESRCH, Errno::EPERM
      end

      # Only the read's end of file ends the loop, never an error from the
      # code the bytes were yielded to.
      def read
        @stdout.readpartial(CHUNK)
      rescue EOFError
        nil
      end
  end

  class Runner
    # stdin: data written to the command's input. timeout: seconds, after
    # which the docker CLI is stopped (its container isn't: callers remove it).
    def call(args, env, stdin: nil, timeout: nil)
      output, status = Open3.capture2e(env, *command(args, timeout), stdin_data: stdin.to_s)
      Result.new(success: status.success?, output:, code: status.exitstatus)
    rescue SystemCallError => e
      Result.new(success: false, output: e.message)
    end

    # from | to, each a docker command. output: both commands' stderr and
    # to's stdout (read as it comes, so a chatty one can't fill the pipe).
    # It succeeds only if both do.
    def pipe(from, to, env, timeout: nil)
      reader, writer = IO.pipe
      collected = Thread.new { reader.read }
      statuses = Open3.pipeline([ env, *command(from, timeout), err: writer ], [ env, *command(to, timeout), out: writer, err: writer ])
      writer.close
      failed = statuses.find { |s| !s.success? }
      Result.new(success: failed.nil?, output: collected.value, code: failed ? failed.exitstatus : 0)
    rescue SystemCallError => e
      Result.new(success: false, output: e.message)
    ensure
      writer&.close unless writer&.closed?
      reader&.close
    end

    # Yields the command's output as it comes. The docker process is killed
    # if the caller stops early (a client that went away) or after timeout.
    def stream(args, timeout: nil)
      Open3.popen2e(*command(args, timeout)) do |stdin, output, wait|
        stdin.close
        begin
          loop { yield output.readpartial(16.kilobytes) }
        rescue EOFError
          wait.value.success?
        ensure
          Process.kill("TERM", wait.pid) if wait.alive?
        end
      end
    end

    # A command whose stdout is a file to send; stderr is kept apart. It
    # waits for the first bytes: a Download, or a failed Result with stderr
    # when the command ended before writing any.
    def download(args, env, timeout: nil)
      stdin, stdout, stderr, wait = Open3.popen3(env, *command(args, timeout), pgroup: true)
      stdin.close
      errors = Thread.new { stderr.read } # read as it comes, so a chatty one can't fill the pipe
      begin
        Download.new(stdout.readpartial(Download::CHUNK), stdout, errors, wait)
      rescue EOFError
        stdout.close
        Result.new(success: false, output: errors.value, code: wait.value.exitstatus)
      end
    rescue SystemCallError => e
      Result.new(success: false, output: e.message)
    end

    private
      def command(args, timeout) = timeout ? [ "timeout", timeout.to_s, "docker", *args ] : [ "docker", *args ]
  end

  class_attribute :runner, default: Runner.new

  def self.run(*args, env: {}, stdin: nil, timeout: nil)
    runner.call(args, env, stdin:, timeout:)
  end

  def self.pipe(from, to, env: {}, timeout: nil)
    runner.pipe(from, to, env, timeout:)
  end

  def self.download(*args, env: {}, timeout: nil)
    runner.download(args, env, timeout:)
  end

  def self.stream(*args, timeout: nil, &block)
    runner.stream(args, timeout:, &block)
  end
end
