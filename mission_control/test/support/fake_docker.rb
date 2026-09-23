# Records docker invocations instead of running them. A responder block can
# return a DockerCommand::Result per call (default: success, no output).
class FakeDocker
  Call = Data.define(:args, :env)

  attr_reader :calls

  def initialize(&responder)
    @calls = []
    @responder = responder
  end

  def call(args, env)
    @calls << Call.new(args:, env:)
    @responder&.call(args, env) || DockerCommand::Result.new(success: true, output: "")
  end

  def all_args = calls.flat_map(&:args)
end

module FakeDockerHelper
  def use_fake_docker(fake)
    original = DockerCommand.runner
    DockerCommand.runner = fake
    yield fake
  ensure
    DockerCommand.runner = original
  end

  def failure(output) = DockerCommand::Result.new(success: false, output:)
end
