require "test_helper"
require "open3"

# lib/update-helper.sh, the script houston-update runs
# (docs/plans/update-from-mission-control.md). Here nsenter and curl are
# fakes: nsenter runs the command it's given (with the fakes on its PATH),
# and curl hands out an installer that says what it was run with, and fails
# for v0.0.1 (a release that doesn't exist: 404).
class UpdateHelperScriptTest < ActiveSupport::TestCase
  SCRIPT = Rails.root.join("lib/update-helper.sh").to_s

  setup do
    @dir = Dir.mktmpdir
    @log = File.join(@dir, "log")
    write "nsenter", <<~'SH'
      #!/bin/sh
      echo "nsenter $*" >> "$FAKE_LOG"
      while [ "$1" != -- ]; do shift; done
      shift
      [ "$1" = env ] && [ "$2" = -i ] || { echo "not a clean environment: $*"; exit 99; }
      shift 2
      case "$1" in PATH=*) shift ;; *) echo "no PATH for the host"; exit 98 ;; esac
      exec env -i PATH="$FAKE_BIN:/usr/bin:/bin" FAKE_LOG="$FAKE_LOG" "$@"
    SH
    write "curl", <<~'SH'
      #!/bin/sh
      while [ $# -gt 0 ]; do case "$1" in -o) out=$2; shift 2 ;; -*) shift ;; *) url=$1; shift ;; esac; done
      echo "curl $url" >> "$FAKE_LOG"
      case "$url" in */v0.0.1/*) exit 22 ;; esac
      printf '%s\n' 'echo "installer: HOUSTON_VERSION=$HOUSTON_VERSION HOUSTON_REPO=$HOUSTON_REPO HOUSTON_DIR=$HOUSTON_DIR HOUSTON_RUNNERS=$HOUSTON_RUNNERS HOME=$HOME secret=${SECRET_KEY_BASE:-none}" >> "$FAKE_LOG"' \
        '[ "$HOUSTON_VERSION" != v0.0.2 ]' > "$out"
    SH
  end

  teardown { FileUtils.rm_rf(@dir) }

  def write(name, body)
    File.write(File.join(@dir, name), body)
    File.chmod(0o755, File.join(@dir, name))
  end

  def helper(to:, from: "v0.4.2")
    env = { "PATH" => "#{@dir}:#{ENV["PATH"]}", "FAKE_BIN" => @dir, "FAKE_LOG" => @log, "SECRET_KEY_BASE" => "leaked",
            "HOUSTON_UPDATE_TO" => to, "HOUSTON_UPDATE_FROM" => from, "HOUSTON_REPO" => "scttymn/houston",
            "HOUSTON_DIR" => "/srv/houston", "HOUSTON_RUNNERS" => "3" }
    output, status = Open3.capture2e(env, "sh", SCRIPT)
    [ status.exitstatus, output, File.exist?(@log) ? File.read(@log) : "" ]
  end

  test "it installs the release on the host, and exits 0" do
    code, output, log = helper(to: "v0.4.3")
    assert_equal 0, code, output
    assert_match %r{^nsenter -t 1 -m -u -i -n -- env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin HOME=/root .* timeout 20m sh -c }, log
    assert_includes log, "curl https://github.com/scttymn/houston/releases/download/v0.4.3/install.sh"
    assert_includes log, "installer: HOUSTON_VERSION=v0.4.3 HOUSTON_REPO=scttymn/houston HOUSTON_DIR=/srv/houston HOUSTON_RUNNERS=3 HOME=/root secret=none"
    assert_equal 1, log.scan("installer:").size
    assert_match "installing v0.4.3", output
  end

  test "a failed install puts the old version back, and exits 3" do
    [ "v0.0.1", "v0.0.2" ].each do |to| # no such release; its installer fails
      File.delete(@log) if File.exist?(@log)
      code, output, log = helper(to:)
      assert_equal 3, code, "#{to}: #{output}"
      assert_includes log, "installer: HOUSTON_VERSION=v0.4.2 ", to
      assert_match "#{to} didn't install; putting v0.4.2 back", output
    end
  end

  test "both failing exits 1, and says what to do" do
    code, output, = helper(to: "v0.0.2", from: "v0.0.1")
    assert_equal 1, code
    assert_match "run the installer on the server", output
  end
end
