# A restore deploy's data: its snapshot put back into the generation the
# restore builds, beside the serving one (docs/plans/restore.md, Batch 4).
# The snapshot goes into a staging volume; its manifest is checked before
# anything is touched; then each volume is emptied and refilled, SQLite
# copies go in place, and each Postgres database is recreated and restored
# into that generation's Postgres. The runner has booted it first.
class RestoreData < DataRun
  # Empties the volume, copies the snapshot's files back, then puts each
  # SQLite copy at its path with no stale -wal/-shm/-journal. Arguments: the
  # volume, then pairs of (staged file, path in the volume).
  FILL = <<~'SH'.squish
    vol="$1"; shift;
    find /v -mindepth 1 -delete &&
    if [ -d "/restore/data/$vol" ]; then cp -a "/restore/data/$vol/." /v/; fi &&
    while [ "$#" -gt 1 ]; do
      mkdir -p "$(dirname "/v/$2")" && cp "/restore/out/$1" "/v/$2" && rm -f "/v/$2-wal" "/v/$2-shm" "/v/$2-journal" || exit 1;
      shift 2;
    done
  SH
  PG_READY = 'pg_isready -U "${POSTGRES_USER:-postgres}" -d postgres'
  # Roles that already exist are fine: psql carries on past their errors.
  PG_ROLES = 'psql -U "${POSTGRES_USER:-postgres}" -d postgres -q'
  # The snapshot's roles carry the superuser's old password; the container's
  # own POSTGRES_PASSWORD (the secret the app uses now) wins. psql quotes :'pw'.
  PG_KEEP_PASSWORD = %q(printf '%s\n' "ALTER ROLE CURRENT_USER WITH PASSWORD :'pw';" | psql -U "${POSTGRES_USER:-postgres}" -d postgres -q -v ON_ERROR_STOP=1 -v pw="$POSTGRES_PASSWORD")
  PG_RECREATE = 'dropdb -U "${POSTGRES_USER:-postgres}" --force --if-exists --maintenance-db=template1 -- "$1" && ' \
                'createdb -U "${POSTGRES_USER:-postgres}" --maintenance-db=template1 -- "$1"'
  PG_RESTORE = 'pg_restore -U "${POSTGRES_USER:-postgres}" --no-owner --role="${POSTGRES_USER:-postgres}" -d "$1"'
  READY_TRIES = 60

  # A file the manifest may name: only what Backup writes.
  STAGED_FILE = %r{\A(postgres/[A-Za-z0-9][A-Za-z0-9_.-]*/(\d+\.dump|globals\.sql)|sqlite/\d+\.sqlite3)\z}

  def initialize(run, token)
    super
    @restore = @project.deploys.find_by!(number: run.deploy_number)
    @target = Generation.new(@project, @restore.generation)
  end

  def call
    run_steps("restore") { restore }
  end

  private
    def restore
      # Defense in depth: the serving generation is never emptied or dropped,
      # whatever the restore's deploy row says.
      serving = @project.serving_generation.number
      raise Failed, "refusing to restore into generation #{serving}: generation #{serving} is the one serving" if @target.number == serving

      VolumePlacement.new(@project, generation: @target.number).place!
      prepare
      ran = docker(*@location.restic_args("restore", @run.source_snapshot_id, "--target", "/restore", name: container(:restic), mounts: [ "#{staging}:/restore" ]),
                   env: @location.restic_env)
      raise Failed, "restic restore failed (exit #{ran.code}): #{tail(ran.output)}" unless ran.success

      manifest = read_manifest
      manifest["volumes"].each { |volume| fill(volume["name"], manifest["sqlite"].select { |s| s["volume"] == volume["name"] }) }
      manifest["postgres"].each { |pg| restore_postgres(pg) }
      finish("go", found: { "volumes" => manifest["volumes"].map { |v| v["name"] },
                            "sqlite" => manifest["sqlite"].map { |s| s.slice("volume", "path") },
                            "databases" => manifest["postgres"].flat_map { |pg| pg["databases"].map { |d| { "service" => pg["service"], "name" => d["name"] } } } })
    rescue VolumePlacement::Refused => e
      raise Failed, "couldn't make generation #{@target.number}'s volumes: #{e.message}"
    end

    # The snapshot's manifest, checked before anything is touched.
    def read_manifest
      read = docker("run", "--rm", "--name", container(:read), "--user", "0", "-v", "#{staging}:/restore:ro",
                    "--entrypoint", "cat", tools, "/restore/out/houston.json")
      raise Failed, "couldn't read the snapshot's manifest: #{tail(read.output)}" unless read.success

      manifest = begin
        JSON.parse(read.output)
      rescue JSON::ParserError
        raise Failed, "the snapshot's manifest isn't JSON"
      end
      raise Failed, "the snapshot's manifest isn't Houston's" unless manifest.is_a?(Hash) && %w[volumes postgres sqlite].all? { |k| manifest[k].is_a?(Array) }
      raise Failed, "the snapshot is of #{manifest["project"]}, not #{@project.name}" unless manifest["project"] == @project.name
      unless manifest["sha"] == @restore.sha
        raise Failed, "the snapshot was taken at #{manifest["sha"].to_s.first(7)}, not the restore's #{@restore.sha.first(7)}"
      end

      files = manifest["postgres"].flat_map { |pg| [ pg["globals"], *pg["databases"].to_a.map { |d| d["file"] } ] } + manifest["sqlite"].map { |s| s["file"] }
      if (bad = files.find { |f| !f.is_a?(String) || !f.match?(STAGED_FILE) })
        raise Failed, "the manifest names #{bad.inspect}, not one Houston writes"
      end
      if (climbs = manifest["sqlite"].find { |s| !s["path"].is_a?(String) || s["path"].start_with?("/") || s["path"].split("/").include?("..") })
        raise Failed, "the SQLite path #{climbs["path"].inspect} leaves its volume"
      end
      volumes = @project.volumes.map { |v| v["name"] }
      if (unknown = manifest["volumes"].map { |v| v["name"] }.find { |v| !volumes.include?(v) })
        raise Failed, "#{@project.name} has no volume #{unknown} (the snapshot's compose.yml differs)"
      end
      manifest
    end

    def fill(volume, sqlite)
      filled = docker("run", "--rm", "--name", container(:fill), "--user", "0", "-v", "#{@target.volume(volume)}:/v", "-v", "#{staging}:/restore:ro",
                      "--entrypoint", "sh", tools, "-c", FILL, "sh", volume, *sqlite.flat_map { |s| [ s["file"], s["path"] ] })
      raise Failed, "couldn't restore the volume #{volume}: #{tail(filled.output)}" unless filled.success
    end

    def restore_postgres(pg)
      service = pg["service"]
      container = @target.container(service)
      ready = READY_TRIES.times.any? do |i|
        sleep 1 if i.positive?
        docker("exec", container, "sh", "-c", PG_READY).success
      end
      raise Failed, "#{service}'s Postgres (#{container}) isn't ready" unless ready

      roles = pipe(feed(pg["globals"]), [ "exec", "-i", container, "sh", "-c", PG_ROLES ])
      raise Failed, "restoring #{service}'s roles failed: #{tail(roles.output)}" unless roles.success
      kept = docker("exec", container, "sh", "-c", PG_KEEP_PASSWORD)
      raise Failed, "setting #{service}'s password back failed: #{tail(kept.output)}" unless kept.success

      pg["databases"].each do |database|
        name = database["name"]
        recreated = docker("exec", container, "sh", "-c", PG_RECREATE, "sh", name)
        raise Failed, %(recreating #{service}'s database "#{name}" failed: #{tail(recreated.output)}) unless recreated.success
        restored = pipe(feed(database["file"]), [ "exec", "-i", container, "sh", "-c", PG_RESTORE, "sh", conninfo(name) ])
        raise Failed, %(pg_restore of #{service}'s database "#{name}" failed: #{tail(restored.output)}) unless restored.success
      end
    end

    # A staged file, streamed out (the first half of a pipe).
    def feed(file) = [ "run", "--rm", "--name", container(:feed), "--user", "0", "-v", "#{staging}:/restore:ro", "--entrypoint", "cat", tools, "/restore/out/#{file}" ]

    # Project names have no dots, so no project's names can match another's.
    def staging = "houston-restore.#{@project.name}"
    def container(role) = "houston-restore.#{@project.name}.#{role}"
    def containers = %i[restic read fill feed].map { |role| container(role) }
end
