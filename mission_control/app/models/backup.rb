# One backup run, as docker commands (see docs/plans/volumes-backups.md,
# Batch 1): dump each Postgres database and copy each SQLite database into a
# staging volume, write a manifest, then one restic snapshot of the app's
# volumes and the staging volume together. The staging volume and helper
# containers are removed on every path. Secrets reach docker only through
# its environment.
class Backup < DataRun
  LIST_DATABASES = %q(psql -U "${POSTGRES_USER:-postgres}" -d postgres -Atc "select encode(convert_to(datname, 'UTF8'), 'hex') from pg_database where not datistemplate order by datname")
  PG_DUMP = 'pg_dump -U "${POSTGRES_USER:-postgres}" -Fc -d "$1"'
  PG_GLOBALS = 'pg_dumpall -U "${POSTGRES_USER:-postgres}" --globals-only'
  # restic reports files it couldn't read; a run lists this many.
  UNREADABLE_SHOWN = 20

  def initialize(run, token)
    super
    @serving = @project.serving_generation
  end

  def call
    if @project.volumes.empty? && @project.databases.empty?
      return finish("skipped", error: "nothing to back up (no named volumes, no Postgres)")
    end

    @sha = @project.running_deploy&.sha || "unknown"
    @run.begin!(@token, sha: @sha)
    run_steps("backup") { back_up }
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
    end

    def dump_postgres(service, image)
      container = @serving.container(service)
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

    def copy_sqlite
      mounts = @project.volumes.flat_map { |v| [ "-v", "#{@serving.volume(v["name"])}:/data/#{v["name"]}" ] }
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
      volumes = @project.volumes.map { |v| "#{@serving.volume(v["name"])}:/data/#{v["name"]}:ro" }
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

    # Helpers run as root: the staging volume is root's, and the app's volumes
    # belong to whoever the app runs as (Mission Control's image is uid 1000).
    def write_to(file) = [ "run", "--rm", "-i", "--name", container(:write), "--user", "0", "-v", "#{staging}:/out", "--entrypoint", "sh", tools, "-c", WRITE, "sh", "/out/#{file}" ]

    # Project names have no dots, so no project's names can match another's.
    def staging = "houston-backup.#{@project.name}"
    def container(role) = "houston-backup.#{@project.name}.#{role}"
    def containers = %i[sqlite restic write].map { |role| container(role) }
end
