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
  # download: what a downloaded command (restic dump) does: the chunks it
  # writes, { chunks:, broken: } for one that fails after them, or a Result
  # for one that fails first; or a proc on the args returning one of those.
  def initialize(stream: [], download: [ "PK\x03\x04zip".b ], &responder)
    @calls = []
    @streams = []
    @downloads = []
    @stream = stream
    @download = download
    @responder = responder
  end

  attr_reader :downloads

  def download(args, env, timeout: nil)
    @calls << Call.new(args:, env:, timeout:)
    scripted = @download.respond_to?(:call) ? @download.call(args) : @download
    return scripted if scripted.is_a?(DockerCommand::Result)

    scripted = { chunks: scripted } if scripted.is_a?(Array)
    FakeDownload.new(**scripted).tap { |d| @downloads << d }
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

# A download that writes its chunks, then fails if broken says so.
class FakeDownload
  attr_reader :first

  def initialize(chunks:, broken: nil)
    @chunks = chunks
    @first = chunks.first
    @broken = broken
    @closed = false
  end

  def each(&)
    @chunks.each(&)
    raise DockerCommand::Broken, @broken if @broken
  end

  def close = @closed = true
  def closed? = @closed
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
