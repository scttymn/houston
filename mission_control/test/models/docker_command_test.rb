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
        zip) printf 'PKzip-'; echo "noise" >&2; sleep 0.1; printf '%s' "$SECRET" ;;
        nothing) echo "Fatal: wrong password" >&2; exit 12 ;;
        breaks) printf 'PKpart'; echo "Fatal: pack gone" >&2; exit 1 ;;
        forever) printf 'PKstart'; exec sleep 30 ;;
        stubborn) trap '' TERM; sleep 30 & printf 'PKstubborn'; wait ;;
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

  test "download keeps stderr out and breaks on failure" do
    download = @runner.download(%w[zip], { "SECRET" => "-rest" })
    assert_equal "PK", download.first.first(2)
    chunks = []
    download.each { |chunk| chunks << chunk }
    download.close
    assert_equal "PKzip--rest", chunks.join, "stdout's bytes only: stderr's noise stays out"

    failed = @runner.download(%w[nothing], {})
    assert_kind_of DockerCommand::Result, failed, "it ended before any bytes: nothing to send"
    assert_equal [ false, 12, "Fatal: wrong password\n" ], [ failed.success, failed.code, failed.output ]

    broken = @runner.download(%w[breaks], {})
    error = assert_raises(DockerCommand::Broken) { broken.each { } }
    assert_includes error.message, "Fatal: pack gone"
    broken.close

    endless = @runner.download(%w[forever], {})
    assert_equal "PKstart", endless.first
    started = Process.clock_gettime(Process::CLOCK_MONOTONIC)
    endless.close
    assert_operator Process.clock_gettime(Process::CLOCK_MONOTONIC) - started, :<, 5, "close stops the command"

    # One that ignores TERM, with a child holding its output open: close
    # still returns, after STOP_WAIT, by killing the whole group.
    stubborn = @runner.download(%w[stubborn], {})
    assert_equal "PKstubborn", stubborn.first
    started = Process.clock_gettime(Process::CLOCK_MONOTONIC)
    stubborn.close
    assert_operator Process.clock_gettime(Process::CLOCK_MONOTONIC) - started, :<, DockerCommand::Download::STOP_WAIT + 3
  end
end
