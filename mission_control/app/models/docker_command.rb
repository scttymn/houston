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
  end

  class_attribute :runner, default: Runner.new

  def self.run(*args, env: {})
    runner.call(args, env)
  end
end
