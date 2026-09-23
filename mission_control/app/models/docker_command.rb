require "open3"

# Runs the docker CLI. Secrets are passed through the environment of the
# docker process (`docker run -e NAME` without a value), never in argv.
# Tests swap the runner for a recording fake.
class DockerCommand
  # code: the exit status (124 when a timeout stopped it).
  Result = Data.define(:success, :output, :code) do
    def initialize(success:, output:, code: success ? 0 : 1) = super
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

  def self.stream(*args, timeout: nil, &block)
    runner.stream(args, timeout:, &block)
  end
end
