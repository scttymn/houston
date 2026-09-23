require "test_helper"

# DockerCommand::Runner for real, with a stand-in `docker` on PATH: the
# process plumbing the recording fake skips (stdin, exit codes, timeouts,
# pipes, the environment).
class DockerCommandTest < ActiveSupport::TestCase
  setup do
    @bin = Dir.mktmpdir
    File.write(File.join(@bin, "docker"), <<~SH)
      #!/bin/sh
      case "$1" in
        echo) shift; echo "$@" ;;
        cat) cat ;;
        env) printenv SECRET ;;
        fail) echo "boom" >&2; exit 7 ;;
        sleep) sleep 5 ;;
        emit) printf 'dump-bytes' ;;
        sink) cat > "$2"; echo "sink says hi" >&2 ;;
      esac
    SH
    File.chmod(0o755, File.join(@bin, "docker"))
    @path = ENV["PATH"]
    ENV["PATH"] = "#{@bin}:#{@path}"
    @runner = DockerCommand::Runner.new
  end

  teardown do
    ENV["PATH"] = @path
    FileUtils.rm_rf(@bin)
  end

  test "the runner passes argv, stdin and the environment, and reports exit codes" do
    assert_equal [ true, "a b\n", 0 ], @runner.call(%w[echo a b], {}).then { |r| [ r.success, r.output, r.code ] }
    assert_equal "from stdin", @runner.call(%w[cat], {}, stdin: "from stdin").output
    assert_equal "hidden\n", @runner.call(%w[env], { "SECRET" => "hidden" }).output
    failed = @runner.call(%w[fail], {})
    assert_equal [ false, "boom\n", 7 ], [ failed.success, failed.output, failed.code ]
  end

  test "a timeout stops the command with exit 124" do
    started = Process.clock_gettime(Process::CLOCK_MONOTONIC)
    result = @runner.call(%w[sleep], {}, timeout: 1)
    assert_equal [ false, 124 ], [ result.success, result.code ]
    assert_operator Process.clock_gettime(Process::CLOCK_MONOTONIC) - started, :<, 4
  end

  test "a pipe carries the bytes and both commands' errors" do
    out = File.join(@bin, "dump")
    piped = @runner.pipe(%w[emit], [ "sink", out ], {})
    assert piped.success, piped.output
    assert_equal "dump-bytes", File.read(out)
    assert_equal "sink says hi\n", piped.output

    failed = @runner.pipe(%w[fail], [ "sink", out ], {})
    assert_equal [ false, 7 ], [ failed.success, failed.code ]
    assert_includes failed.output, "boom"
  end
end
