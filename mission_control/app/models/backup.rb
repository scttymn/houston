# One backup run, as docker commands (see docs/plans/volumes-backups.md,
# Batch 1): dump each Postgres database and copy each SQLite database into a
# staging volume, write a manifest, then one restic snapshot of the app's
# volumes and the staging volume together. The staging volume and helper
# containers are removed on every path. Secrets reach docker only through
# its environment.
class Backup
  DEADLINE = 3.hours
  class_attribute :heartbeat_every, default: 15.seconds

  WRITE = 'mkdir -p "$(dirname "$1")" && cat > "$1"'
  LIST_DATABASES = %q(psql -U "${POSTGRES_USER:-postgres}" -d postgres -Atc "select encode(convert_to(datname, 'UTF8'), 'hex') from pg_database where not datistemplate order by datname")
  PG_DUMP = 'pg_dump -U "${POSTGRES_USER:-postgres}" -Fc -d "$1"'
  PG_GLOBALS = 'pg_dumpall -U "${POSTGRES_USER:-postgres}" --globals-only'
  # restic reports files it couldn't read; a run lists this many.
  UNREADABLE_SHOWN = 20

  class Failed < StandardError; end
  class TimedOut < StandardError; end

  def initialize(run, token)
    @run = run
    @token = token
    @project = run.project
    @location = run.location
    @warnings = []
    @log = +""
  end

  def call
    if @project.volumes.empty? && @project.databases.empty?
      return finish("skipped", error: "nothing to back up (no named volumes, no Postgres)")
    end

    @started = Process.clock_gettime(Process::CLOCK_MONOTONIC)
    @sha = @project.running_deploy&.sha || "unknown"
    @run.begin!(@token, sha: @sha)
    with_heartbeat { back_up }
  end

  private
    def back_up
      prepare
      postgres = @project.databases.map { |db| dump_postgres(db["service"], db["image"]) }
      sqlite = @project.volumes.any? ? copy_sqlite : []
      write_manifest(postgres, sqlite)
      snapshot_id, bytes = restic_backup
      forget
      found = { "databases" => postgres.flat_map { |pg| pg[:databases].map { |d| { "service" => pg[:service], "name" => d[:name] } } },
                "sqlite" => sqlite.map { |s| s.slice("volume", "path") } }
      finish("go", error: @warnings.presence&.join("; "), snapshot_id:, bytes:, found:)
      Snapshots.forget_cache(@project, @location)
    rescue Failed => e
      finish("no_go", error: e.message)
    rescue TimedOut
      clean_up("rm", "-f", *containers)
      finish("no_go", error: "took longer than #{DEADLINE.inspect}; stopped")
    rescue StandardError => e
      # A bug or a surprise: the run still finishes (the job then fails loudly).
      finish("no_go", error: "Houston failed during the backup: #{e.class}: #{e.message}")
      raise
    ensure
      clean_up("volume", "rm", "-f", staging)
    end

    def prepare
      docker("rm", "-f", *containers)
      docker("volume", "rm", "-f", staging)
      created = docker("volume", "create", staging)
      raise Failed, "couldn't create the staging volume: #{tail(created.output)}" unless created.success
    end

    def dump_postgres(service, image)
      container = "#{@project.name}-#{service}"
      listed = docker("exec", container, "sh", "-c", LIST_DATABASES)
      lines = listed.output.lines.map(&:strip).reject(&:empty?)
      unless listed.success && lines.all? { |l| l.match?(/\A\h+\z/) }
        raise Failed, "couldn't list #{service}'s databases: #{tail(listed.output)}"
      end

      databases = lines.each_with_index.map do |line, i|
        name = [ line ].pack("H*").force_encoding(Encoding::UTF_8).scrub
        file = "postgres/#{service}/#{i + 1}.dump"
        dumped = pipe([ "exec", container, "sh", "-c", PG_DUMP, "sh", conninfo([ line ].pack("H*")) ], write_to(file))
        raise Failed, %(pg_dump of #{service}'s database "#{name}" failed: #{tail(dumped.output)}) unless dumped.success
        { name:, file: }
      end
      globals = "postgres/#{service}/globals.sql"
      dumped = pipe([ "exec", container, "sh", "-c", PG_GLOBALS ], write_to(globals))
      raise Failed, "pg_dumpall --globals-only of #{service} failed: #{tail(dumped.output)}" unless dumped.success
      { service:, image:, globals:, databases: }
    end

    # A libpq connection string naming the database, so a name with "=" or
    # a URI prefix can't be read as connection options.
    def conninfo(name) = "dbname='#{name.b.gsub(/[\\']/) { "\\#{$&}" }}'"

    def copy_sqlite
      mounts = @project.volumes.flat_map { |v| [ "-v", "#{@project.name}_#{v["name"]}:/data/#{v["name"]}" ] }
      ran = docker("run", "--rm", "--name", container(:sqlite), "--user", "0", *mounts, "-v", "#{staging}:/out",
                   "--entrypoint", "ruby", tools, "/rails/lib/backup/sqlite.rb", "/data", "/out")
      result = begin
        JSON.parse(ran.output.lines.last.to_s)
      rescue JSON::ParserError
        nil
      end
      raise Failed, "looking for SQLite databases failed: #{tail(ran.output)}" unless result.is_a?(Hash)
      if (error = result["errors"]&.first)
        raise Failed, "couldn't copy the SQLite database #{error["volume"]}/#{error["path"]}: #{error["message"]}"
      end
      raise Failed, "looking for SQLite databases failed: #{tail(ran.output)}" unless ran.success

      @warnings.concat(result["warnings"].to_a)
      result["sqlite"].to_a
    end

    def write_manifest(postgres, sqlite)
      manifest = { version: 1, project: @project.name, sha: @sha, volumes: @project.volumes,
                   postgres: postgres.map { |pg| { service: pg[:service], image: pg[:image], globals: pg[:globals], databases: pg[:databases] } },
                   sqlite: }
      written = docker(*write_to("houston.json"), stdin: JSON.pretty_generate(manifest))
      raise Failed, "couldn't write the manifest: #{tail(written.output)}" unless written.success
    end

    def restic_backup
      tags = [ "project:#{@project.name}", "sha:#{@sha}", "kind:#{@run.kind}", "reason:#{@run.reason}" ]
      tags << "deploy:#{@run.deploy_number}" if @run.deploy_number
      volumes = @project.volumes.map { |v| "#{@project.name}_#{v["name"]}:/data/#{v["name"]}:ro" }
      # --retry-lock: a pre-deploy snapshot can meet the daily prune's exclusive lock.
      command = [ "backup", "--retry-lock", "10m", "--host", "houston", "--json", *tags.flat_map { |t| [ "--tag", t ] } ]
      command += [ "--exclude-file", "/out/.houston/exclude", "/data" ] if volumes.any?
      command << "/out"
      ran = docker(*@location.restic_args(*command, name: container(:restic), mounts: volumes + [ "#{staging}:/out:ro" ]), env: @location.restic_env)

      messages = ran.output.lines.filter_map { |line| JSON.parse(line) rescue nil }.select { |m| m.is_a?(Hash) }
      summary = messages.find { |m| m["message_type"] == "summary" }
      unless (ran.success || ran.code == 3) && summary
        raise Failed, "restic backup failed (exit #{ran.code}): #{tail(ran.output)}"
      end
      if ran.code == 3
        items = messages.select { |m| m["message_type"] == "error" }.map { |m| m["item"] || m.dig("error", "message") }
        noun = items.size == 1 ? "file" : "files"
        @warnings << "#{items.size} #{noun} couldn't be read: #{items.first(UNREADABLE_SHOWN).join(", ")}#{items.size > UNREADABLE_SHOWN ? ", …" : ""}"
      end
      [ summary["snapshot_id"], summary["total_bytes_processed"] ]
    end

    # Retention for this run's kind (spec §9): the newest snapshot per day for
    # auto, the last N for deploy. Grouping by nothing matters: restic's
    # default (host and paths) would keep every snapshot whose paths differ.
    # A failure is a warning; the new snapshot is safe and the next run tries again.
    def forget
      keep = @run.kind == "deploy" ? [ "--keep-last", @project.keep_deploy.to_s ] : [ "--keep-daily", @project.keep_auto.to_s ]
      ran = docker(*@location.restic_args("forget", "--retry-lock", "10m", "--host", "houston", "--tag", "project:#{@project.name},kind:#{@run.kind}", "--group-by", "", "--json", *keep),
                   env: @location.restic_env)
      @warnings << "old snapshots weren't forgotten: #{tail(ran.output)}" unless ran.success
    end

    def finish(status, **fields)
      Rails.logger.warn("backup of #{@project.name}: run #{@run.id} was taken over; not finishing it") unless
        @run.finish!(@token, status:, log: @log, **fields)
    end

    # Each command gets what's left of the deadline; a timeout (exit 124)
    # stops the whole backup.
    def docker(*args, env: {}, stdin: nil)
      @log << "$ docker #{args.first(4).join(" ")} …\n"
      result = DockerCommand.run(*args, env:, stdin:, timeout: remaining)
      raise TimedOut if result.code == 124
      result
    end

    # Cleanup runs even after the deadline, briefly, and never raises.
    def clean_up(*args) = DockerCommand.run(*args, timeout: 60)

    def pipe(from, to)
      @log << "$ docker #{from.first(3).join(" ")} … | docker run … #{to.last}\n"
      result = DockerCommand.pipe(from, to, timeout: remaining)
      raise TimedOut if result.code == 124
      result
    end

    def remaining
      left = (DEADLINE - (Process.clock_gettime(Process::CLOCK_MONOTONIC) - @started)).to_i
      raise TimedOut if left < 1
      left
    end

    # Helpers run as root: the staging volume is root's, and the app's volumes
    # belong to whoever the app runs as (Mission Control's image is uid 1000).
    def write_to(file) = [ "run", "--rm", "-i", "--name", container(:write), "--user", "0", "-v", "#{staging}:/out", "--entrypoint", "sh", tools, "-c", WRITE, "sh", "/out/#{file}" ]

    # Project names have no dots, so no project's names can match another's.
    def staging = "houston-backup.#{@project.name}"
    def container(role) = "houston-backup.#{@project.name}.#{role}"
    def containers = %i[sqlite restic write].map { |role| container(role) }
    def tools = ENV.fetch("HOUSTON_TOOLS_IMAGE", "houston/mission-control:local")

    def tail(output) = output.to_s.lines.reject { |l| l.start_with?('{"message_type":"status"') }.last(20).join.strip.truncate(2000)

    # The run's heartbeat, from a thread, while the backup runs.
    def with_heartbeat
      beat = Thread.new do
        loop do
          sleep heartbeat_every
          ActiveRecord::Base.connection_pool.with_connection { @run.heartbeat!(@token) }
        end
      end
      yield
    ensure
      beat&.kill&.join
    end
end
