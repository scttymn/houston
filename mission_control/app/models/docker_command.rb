require "open3"

# Runs the docker CLI. Secrets are passed through the environment of the
# docker process (`docker run -e NAME` without a value), never in argv.
# Tests swap the runner for a recording fake.
class DockerCommand
  Result = Data.define(:success, :output)

  class Runner
    def call(args, env)
      output, status = Open3.capture2e(env, "docker", *args)
      Result.new(success: status.success?, output:)
    rescue SystemCallError => e
      Result.new(success: false, output: e.message)
    end

    # Yields the command's output as it comes. The docker process is killed
    # if the caller stops early (a client that went away) or after timeout.
    def stream(args, timeout: nil)
      command = timeout ? [ "timeout", timeout.to_s, "docker", *args ] : [ "docker", *args ]
      Open3.popen2e(*command) do |stdin, output, wait|
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
  end

  class_attribute :runner, default: Runner.new

  def self.run(*args, env: {})
    runner.call(args, env)
  end

  def self.stream(*args, timeout: nil, &block)
    runner.stream(args, timeout:, &block)
  end
end
