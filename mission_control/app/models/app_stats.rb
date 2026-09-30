require "open3"

# Each app's CPU, memory and disk right now, from Docker, for the flight
# board (docs/plans/app-stats.md). AppStatsJob samples every 30 seconds and
# keeps the readings in the cache; pages only read them, never Docker.
#
# An app's containers are Houston's own names: its web containers
# <project>-web-<sha>, its accessories <project>-<service>[-g<n>], and its
# volumes <project>[.g<n>]_<name> (Generation). CPU is in cores; a limit
# counts only when every running container of the app has one. Without one,
# a share is of the host's: its cores and memory (docker info), and the disk
# Docker keeps its volumes on (the one Mission Control's container is on).
module AppStats
  CACHE_KEY = "app-stats".freeze
  STALE_AFTER = 2.minutes
  DISK_EVERY = 5.minutes
  TIMEOUT = 20

  Reading = Data.define(:cpu_cores, :cpu_limit, :memory_bytes, :memory_limit, :disk_bytes, :sampled_at, :host) do
    def stale? = sampled_at < STALE_AFTER.ago

    # What a share is of: the limit, or, without one, the host's (nil when
    # Mission Control doesn't know it). Of the host? says which.
    def cpu_whole = cpu_limit&.positive? ? cpu_limit : host_value("cpus")
    def memory_whole = memory_limit&.positive? ? memory_limit : host_value("memory")
    def disk_whole = host_value("disk")
    def cpu_of_host? = !cpu_limit&.positive? && !cpu_whole.nil?
    def memory_of_host? = !memory_limit&.positive? && !memory_whole.nil?

    # Percent of the whole, unrounded (the board rounds, and writes <1%).
    def cpu_percent = percent(cpu_cores, cpu_whole)
    def memory_percent = percent(memory_bytes, memory_whole)
    def disk_percent = percent(disk_bytes, disk_whole)

    def as_json(*) = { cpu_cores: cpu_cores.round(4), cpu_limit:, memory_bytes:, memory_limit:, disk_bytes:, sampled_at: }

    private

    def host_value(key) = (v = host&.dig(key)) && v.positive? ? v : nil
    def percent(part, whole) = whole ? 100.0 * part / whole : nil
  end

  # The project's latest reading, or nil when nothing of it was running.
  def self.for(project)
    kept = Rails.cache.read(CACHE_KEY) or return
    row = kept.dig("projects", project.name) or return
    Reading.new(cpu_cores: row["cpu_cores"], cpu_limit: row["cpu_limit"], memory_bytes: row["memory_bytes"],
                memory_limit: row["memory_limit"], disk_bytes: row["disk_bytes"], sampled_at: Time.zone.parse(kept["sampled_at"]),
                host: kept["host"])
  end

  # Reads Docker and keeps a reading per project; the board refreshes when
  # what it shows changed. Docker failing keeps the last readings.
  def self.sample!(projects = Project.all.to_a)
    kept = Rails.cache.read(CACHE_KEY) || {}
    containers = read_containers or return
    disk, host = kept["disk"], (kept["host"] || {}).dup
    disk_at = kept["disk_sampled_at"] && Time.zone.parse(kept["disk_sampled_at"])
    # The host's cores and memory each time (docker info is quick); its disk's
    # size with the volumes (slow: every 5 minutes), or now when it's missing.
    host.merge!(read_machine || {})
    if disk.nil? || disk_at.nil? || disk_at < DISK_EVERY.ago
      if (fresh = read_volumes)
        disk, disk_at = fresh, Time.current
      end
      host["disk"] = disk_size.call || host["disk"]
    end
    host["disk"] ||= disk_size.call

    rows = projects.each_with_object({}) do |project, out|
      mine = containers.select { |c| app_container?(project, c[:name]) }
      next if mine.empty?
      out[project.name] = {
        "cpu_cores" => mine.sum { it[:cpu] },
        "cpu_limit" => (mine.sum { it[:nano_cpus] } / 1e9 if mine.all? { it[:nano_cpus].positive? }),
        "memory_bytes" => mine.sum { it[:memory] },
        "memory_limit" => (mine.sum { it[:memory_limit] } if mine.all? { it[:memory_limit].positive? }),
        "disk_bytes" => (disk || {}).sum { |name, bytes| app_volume?(project, name) ? bytes : 0 }
      }
    end
    Rails.cache.write(CACHE_KEY, { "sampled_at" => Time.current.iso8601, "projects" => rows, "disk" => disk,
                                   "disk_sampled_at" => disk_at&.iso8601, "host" => host })
    # The board redraws when what it shows changed: an app's numbers, or what
    # a share is of.
    FlightBoard.refresh! if shown(rows) != shown(kept["projects"] || {}) || host != (kept["host"] || {})
  end

  def self.app_container?(project, name)
    name.match?(/\A#{Regexp.escape(project.name)}-web-\h{7,}\z/) ||
      project.accessories.any? { |service| name.match?(/\A#{Regexp.escape(project.name)}-#{Regexp.escape(service)}(-g\d+)?\z/) }
  end
  private_class_method :app_container?

  def self.app_volume?(project, name) = name.match?(/\A#{Regexp.escape(project.name)}(\.g\d+)?_./)
  private_class_method :app_volume?

  # What the board shows, rounded as it shows it: a refresh only when that changes.
  def self.shown(rows)
    rows.transform_values do |r|
      [ (r["cpu_cores"] * 100).round, r["cpu_limit"], (r["memory_bytes"] / 1.megabyte).round, r["memory_limit"], (r["disk_bytes"] / 1.megabyte).round ]
    end
  end
  private_class_method :shown

  # Running containers: name, CPU (cores), memory used, and their limits
  # (0 when none). nil when Docker can't say.
  def self.read_containers
    stats = DockerCommand.run("stats", "--no-stream", "--format", "{{json .}}", timeout: TIMEOUT)
    return failed("docker stats", stats) unless stats.success
    rows = stats.output.lines.filter_map { |line| JSON.parse(line) rescue nil }
    return [] if rows.empty?

    inspected = DockerCommand.run("inspect", "--format", "{{slice .Id 0 12}} {{.HostConfig.Memory}} {{.HostConfig.NanoCpus}}", *rows.map { it["ID"] }, timeout: TIMEOUT)
    return failed("docker inspect", inspected) unless inspected.success
    limits = inspected.output.lines.to_h { |line| id, memory, cpus = line.split; [ id, [ memory.to_i, cpus.to_i ] ] }

    rows.map do |row|
      memory_limit, nano_cpus = limits.fetch(row["ID"].to_s[0, 12], [ 0, 0 ])
      { name: row["Name"].to_s, cpu: row["CPUPerc"].to_f / 100, memory: bytes(row["MemUsage"].to_s.split("/").first),
        memory_limit:, nano_cpus: }
    end
  end
  private_class_method :read_containers

  # Each volume's size in bytes; nil when Docker can't say.
  def self.read_volumes
    df = DockerCommand.run("system", "df", "-v", "--format", "{{json .Volumes}}", timeout: TIMEOUT)
    return failed("docker system df", df) unless df.success
    JSON.parse(df.output).to_h { |v| [ v["Name"].to_s, bytes(v["Size"]) ] }
  rescue JSON::ParserError => e
    failed("docker system df", DockerCommand::Result.new(success: false, output: e.message))
  end
  private_class_method :read_volumes

  # The host's cores and memory (docker info); nil when Docker can't say.
  def self.read_machine
    info = DockerCommand.run("info", "--format", "{{.NCPU}} {{.MemTotal}}", timeout: TIMEOUT)
    return failed("docker info", info) unless info.success
    cpus, memory = info.output.split.map(&:to_i)
    { "cpus" => cpus, "memory" => memory }
  end
  private_class_method :read_machine

  # The size of the disk under Mission Control's container: the one Docker
  # keeps its volumes on (the host's /var/lib/docker). nil when df can't say.
  # Tests set it.
  mattr_accessor :disk_size, default: -> do
    out, status = Open3.capture2("df", "-Pk", "/")
    status.success? ? out.lines.last.split[1].to_i * 1024 : nil
  rescue SystemCallError
    nil
  end

  UNITS = { "B" => 1, "kB" => 1e3, "KB" => 1e3, "MB" => 1e6, "GB" => 1e9, "TB" => 1e12,
            "KiB" => 1024, "MiB" => 1024**2, "GiB" => 1024**3, "TiB" => 1024**4 }.freeze

  # "310.2MiB", "48.2MB", "0B" → bytes (Docker writes both kinds of unit).
  def self.bytes(text)
    number, unit = text.to_s.strip.match(/\A([\d.]+)\s*([A-Za-z]+)\z/)&.captures
    number ? (number.to_f * UNITS.fetch(unit, 0)).round : 0
  end

  def self.failed(what, result)
    Rails.logger.warn("houston: couldn't read the apps' stats (#{what}): #{result.output.to_s.strip.lines.last}")
    nil
  end
  private_class_method :failed
end
