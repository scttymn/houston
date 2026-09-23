# Records docker invocations instead of running them. A responder block can
# return a DockerCommand::Result per call (default: success, no output).
class FakeDocker
  # A piped command (docker exec … | docker run -i …) is recorded as one
  # call: from's args, "|", to's args.
  Call = Data.define(:args, :env, :stdin, :timeout) do
    def initialize(args:, env:, stdin: nil, timeout: nil) = super
  end

  attr_reader :calls, :streams

  # stream: the chunks a streamed command (docker logs) writes.
  def initialize(stream: [], &responder)
    @calls = []
    @streams = []
    @stream = stream
    @responder = responder
  end

  def stream(args, timeout: nil)
    @streams << args
    @stream.each { |chunk| yield chunk }
    true
  end

  def call(args, env, stdin: nil, timeout: nil)
    @calls << Call.new(args:, env:, stdin:, timeout:)
    @responder&.call(args, env) || DockerCommand::Result.new(success: true, output: "")
  end

  def pipe(from, to, env, timeout: nil)
    call(from + [ "|" ] + to, env, timeout:)
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

  def failure(output, code: 1) = DockerCommand::Result.new(success: false, output:, code:)
end
