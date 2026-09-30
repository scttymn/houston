require "test_helper"
require_relative "../support/fake_docker"
require_relative "../support/project_helpers"

# Each app's CPU, memory and disk, from Docker, for the flight board
# (docs/plans/app-stats.md).
class AppStatsTest < ActiveSupport::TestCase
  include FakeDockerHelper
  include ProjectHelpers
  include Turbo::Broadcastable::TestHelper

  SHA = "a" * 40
  GIB = 1024**3
  MIB = 1024**2

  setup do
    @equip = make_project("equip", services: %w[app db])
    @cart = make_project("cart")
    @disk_size, AppStats.disk_size = AppStats.disk_size, -> { 100 * GIB }
  end

  teardown { AppStats.disk_size = @disk_size }

  STATS = [
    { "ID" => "w1", "Name" => "equip-web-#{"a" * 40}", "CPUPerc" => "12.50%", "MemUsage" => "300MiB / 1GiB" },
    { "ID" => "d1", "Name" => "equip-db", "CPUPerc" => "2.00%", "MemUsage" => "100MiB / 512MiB" },
    { "ID" => "d2", "Name" => "equip-db-g2", "CPUPerc" => "1.00%", "MemUsage" => "50MiB / 125.7GiB" },
    { "ID" => "c1", "Name" => "cart-web-#{"b" * 40}", "CPUPerc" => "3.00%", "MemUsage" => "12MiB / 125.7GiB" },
    # Not an app's: Houston's own, the proxy, a look-alike and an unknown service.
    { "ID" => "m1", "Name" => "houston-mission-control-1", "CPUPerc" => "50.00%", "MemUsage" => "900MiB / 125.7GiB" },
    { "ID" => "k1", "Name" => "kamal-proxy", "CPUPerc" => "1.00%", "MemUsage" => "20MiB / 125.7GiB" },
    { "ID" => "x1", "Name" => "equip-web-web-#{"c" * 40}", "CPUPerc" => "9.00%", "MemUsage" => "9MiB / 125.7GiB" },
    { "ID" => "x2", "Name" => "equip-redis", "CPUPerc" => "9.00%", "MemUsage" => "9MiB / 125.7GiB" }
  ].map(&:to_json).join("\n").freeze

  DF = [ { "Name" => "equip_storage", "Size" => "48.2MB" }, { "Name" => "equip.g2_pgdata", "Size" => "1.5GB" },
         { "Name" => "equipment_x", "Size" => "9GB" }, { "Name" => "cart_data", "Size" => "0B" } ].to_json.freeze

  # limits: container ID → [memory bytes, nano CPUs]; stats/df: what those
  # commands print, or a failure.
  def docker(stats: STATS, limits: {}, df: DF, info: "8 #{16 * GIB}")
    FakeDocker.new do |args|
      case args.first
      when "stats" then stats.is_a?(DockerCommand::Result) ? stats : DockerCommand::Result.new(success: true, output: stats)
      when "inspect"
        ids = args.drop(3)
        DockerCommand::Result.new(success: true, output: ids.map { |id| "#{id} #{limits.dig(id, 0) || 0} #{limits.dig(id, 1) || 0}" }.join("\n"))
      when "system" then df.is_a?(DockerCommand::Result) ? df : DockerCommand::Result.new(success: true, output: df)
      when "info" then info.is_a?(DockerCommand::Result) ? info : DockerCommand::Result.new(success: true, output: info)
      end
    end
  end

  def reading(project) = AppStats.for(project)

  test "containers add up per project" do
    use_fake_docker(docker) { AppStats.sample! }
    equip = reading(@equip)
    assert_in_delta 0.155, equip.cpu_cores, 0.0001, "web 12.5% + db 2% + the restored db 1%, of one core each"
    assert_equal 450 * MIB, equip.memory_bytes
    assert_in_delta 0.03, reading(@cart).cpu_cores, 0.0001
    assert_equal 12 * MIB, reading(@cart).memory_bytes

    idle = make_project("idle")
    assert_nil reading(idle), "nothing running, nothing read"
  end

  test "a limit only when every container has one" do
    some = { "w1" => [ GIB, 2_000_000_000 ], "d1" => [ 512 * MIB, 1_000_000_000 ] } # the restored db has none
    use_fake_docker(docker(limits: some)) { AppStats.sample! }
    assert_nil reading(@equip).memory_limit
    assert_nil reading(@equip).cpu_limit

    all = some.merge("d2" => [ 256 * MIB, 500_000_000 ])
    use_fake_docker(docker(limits: all)) { AppStats.sample! }
    assert_equal GIB + 768 * MIB, reading(@equip).memory_limit
    assert_in_delta 3.5, reading(@equip).cpu_limit, 0.0001
    assert_nil reading(@cart).memory_limit
  end

  # Without a limit, a share is of the host's cores, memory and disk; a limit
  # wins when every container has one.
  test "shares of the limit, or of the host" do
    use_fake_docker(docker(limits: { "c1" => [ 64 * MIB, 500_000_000 ] })) { AppStats.sample! }
    equip = reading(@equip)
    assert equip.cpu_of_host?
    assert_equal 8, equip.cpu_whole
    assert_in_delta 100 * 0.155 / 8, equip.cpu_percent, 0.0001
    assert equip.memory_of_host?
    assert_in_delta 100.0 * 450 * MIB / (16 * GIB), equip.memory_percent, 0.0001
    assert_in_delta 100.0 * (48_200_000 + 1_500_000_000) / (100 * GIB), equip.disk_percent, 0.0001
    cart = reading(@cart)
    assert_not cart.cpu_of_host?
    assert_in_delta 100 * 0.03 / 0.5, cart.cpu_percent, 0.0001, "its limit, not the host"
    assert_in_delta 100.0 * 12 / 64, cart.memory_percent, 0.0001

    # The host's cores and memory are read each time; when Docker can't say,
    # what was read stays, and before anything was, no share.
    travel 30.seconds
    use_fake_docker(docker(info: "16 #{32 * GIB}")) { AppStats.sample! }
    assert_equal 16, reading(@equip).cpu_whole, "each sample, not with the disk"
    use_fake_docker(docker(info: failure("no"))) { AppStats.sample! }
    assert_equal 16, reading(@equip).cpu_whole
    Rails.cache.delete(AppStats::CACHE_KEY)
    use_fake_docker(docker(info: failure("no"))) { AppStats.sample! }
    assert_nil reading(@equip).cpu_percent
  end

  # The disk's size is read with the volumes, every 5 minutes, or at once when
  # a reading has none (one kept from before Mission Control read it).
  test "the host's disk, with the volumes or when missing" do
    use_fake_docker(docker) { AppStats.sample! }
    Rails.cache.write(AppStats::CACHE_KEY, Rails.cache.read(AppStats::CACHE_KEY).merge("host" => nil))
    AppStats.disk_size = -> { 200 * GIB }
    use_fake_docker(docker) { |fake| AppStats.sample!; @df = fake.calls.count { |c| c.args.first == "system" } }
    assert_equal 0, @df, "the volumes aren't due"
    assert_equal 200 * GIB, reading(@equip).disk_whole, "but the missing size is read now"
    AppStats.disk_size = -> { 300 * GIB }
    travel 1.minute
    use_fake_docker(docker) { AppStats.sample! }
    assert_equal 200 * GIB, reading(@equip).disk_whole, "and kept until the volumes are read again"
    travel 5.minutes
    use_fake_docker(docker) { AppStats.sample! }
    assert_equal 300 * GIB, reading(@equip).disk_whole
  end

  test "disk per project, now and then" do
    use_fake_docker(docker) { |fake| AppStats.sample!; @df = fake.calls.count { |c| c.args.first == "system" } }
    assert_equal 1, @df
    assert_equal 48_200_000 + 1_500_000_000, reading(@equip).disk_bytes
    assert_equal 0, reading(@cart).disk_bytes

    travel 4.minutes
    use_fake_docker(docker(df: "[]")) { |fake| AppStats.sample!; @df = fake.calls.count { |c| c.args.first == "system" } }
    assert_equal 0, @df, "within 5 minutes, disk isn't read again"
    assert_equal 48_200_000 + 1_500_000_000, reading(@equip).disk_bytes, "and what was read stays"

    travel 2.minutes
    use_fake_docker(docker(df: "[]")) { |fake| AppStats.sample!; @df = fake.calls.count { |c| c.args.first == "system" } }
    assert_equal 1, @df
    assert_equal 0, reading(@equip).disk_bytes
  end

  test "a failed sample keeps what we had" do
    use_fake_docker(docker) { AppStats.sample! }
    logs = StringIO.new
    old, Rails.logger = Rails.logger, ActiveSupport::Logger.new(logs)
    travel 1.minute
    use_fake_docker(docker(stats: failure("Cannot connect to the Docker daemon"), df: failure("no"))) { AppStats.sample! }
    assert_equal 450 * MIB, reading(@equip).memory_bytes
    assert_not reading(@equip).stale?
    assert_match "couldn't read the apps' stats", logs.string

    # Past 5 minutes, CPU and memory read fine and disk doesn't: disk keeps what it had.
    travel 5.minutes
    use_fake_docker(docker(df: failure("timed out"))) { AppStats.sample! }
    assert_equal 48_200_000 + 1_500_000_000, reading(@equip).disk_bytes

    travel 3.minutes
    assert reading(@equip).stale?, "over 2 minutes old"
  ensure
    Rails.logger = old if old
  end

  test "the board refreshes when the numbers change" do
    count = ->(&block) { capture_turbo_stream_broadcasts(FlightBoard::STREAM, &block).count { |s| s["action"] == "refresh" } }
    assert_equal 1, count.call { use_fake_docker(docker) { AppStats.sample! } }
    assert_equal 0, count.call { use_fake_docker(docker) { AppStats.sample! } }, "the same numbers"
    busier = STATS.sub("300MiB / 1GiB", "600MiB / 1GiB")
    assert_equal 1, count.call { use_fake_docker(docker(stats: busier)) { AppStats.sample! } }
    assert_equal 1, count.call { use_fake_docker(docker(stats: busier, info: "16 #{16 * GIB}")) { AppStats.sample! } }, "the host changed: every share did"
  end
end
