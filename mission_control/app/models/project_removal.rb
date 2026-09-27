# Carries out a ProjectDeletion (docs/plans/delete-project.md), step by step:
#
#   check, snapshot    nothing removed yet: a failure cancels the deletion
#                      and the project serves as before
#   routes … rows      removal: a failure stops the deletion there, the
#                      project stays deleting, and asking again resumes
#
# Every removal step is idempotent (what's already gone is done), and names
# only what's the project's own: exact names, the exact service label, the
# exact DNS comment. Project names are DNS labels (no dots, no underscores),
# so <name>_… and <name>.g<n>_… volumes can only be this project's.
class ProjectRemoval
  class Cancel < StandardError; end
  class Stop < StandardError; end

  REMOVAL_STEPS = %w[routes dns containers volumes folders images registry files backups rows].freeze

  # Removes a project's volume folders on a storage location: volumes/<name>
  # and volumes/<name>.g<n>. The name comes in as $1, never in the script.
  REMOVE_FOLDERS = 'cd /location/volumes 2>/dev/null || exit 0; for d in "$1" "$1".g[0-9]*; do [ ! -e "$d" ] || rm -rf -- "$d" || exit 1; done'
  # Removes Kamal's state for the project (its env files hold secret values)
  # and each runner's checkout of it.
  REMOVE_FILES = 'for p in /kamal/apps/"$1" /kamal/"$1"-audit.log /kamal/lock-"$1" /runners/*/"$1"; do [ ! -e "$p" ] || rm -rf -- "$p" || exit 1; done'
  RUNNERS_DIR = "/var/lib/houston/runners"
  HEARTBEAT_EVERY = 15.seconds
  TIMEOUT = 5.minutes.to_i
  RESTIC_TIMEOUT = 3.hours.to_i

  def initialize(deletion)
    @deletion = deletion
    @log = deletion.log.to_s.dup
  end

  def call
    return unless claim!

    @project = @deletion.project
    with_heartbeat do
      if @project
        prepare unless @deletion.removing?
        remove
      end
      finish("go")
    end
    RegistryCleanupJob.perform_later(@deletion)
  rescue Cancel => e
    finish("no_go", error: "cancelled: #{e.message}")
  rescue Stop => e
    finish("no_go", error: "stopped at #{@deletion.step}: #{e.message}")
  rescue StandardError => e
    finish("no_go", error: "Houston failed while deleting at #{@deletion.step}: #{e.class}: #{e.message}")
    raise
  end

  private
    def installation = @installation ||= Installation.current

    # Flips the deletion from queued to running; false when another job has it.
    def claim!
      now = Time.current
      claimed = ProjectDeletion.where(id: @deletion.id, status: "queued").update_all(status: "running", started_at: now, heartbeat_at: now, updated_at: now)
      @deletion.reload
      @started_at = @deletion.started_at
      FlightBoard.refresh! if claimed == 1
      claimed == 1
    end

    # Writes only while the deletion is still this job's (asking again takes
    # a silent one over).
    def ours = ProjectDeletion.where(id: @deletion.id, status: "running", started_at: @started_at)

    def write(**fields)
      ours.update_all(log: @log.last(ProjectDeletion::LOG_CAP), updated_at: Time.current, **fields)
      @deletion.reload
    end

    def step(name)
      say("== #{name}")
      write(step: name)
      FlightBoard.refresh!
    end

    def say(line) = @log << line << "\n"

    def finish(status, error: nil)
      say(error ? error : "GO: #{@deletion.name} is deleted")
      write(status:, error: error&.truncate(4000), finished_at: Time.current)
      FlightBoard.refresh!
    end

    # ── Before anything is removed ────────────────────────────────────────

    def prepare
      step("check")
      check_docker
      raise Cancel, "Mission Control doesn't know where Kamal keeps its files: run the installer once more" if kamal_home.blank?
      raise Cancel, "the registry doesn't allow deletes yet: run the installer once more" unless Registry.new.deletes_enabled?
      check_cloudflare if cloudflare?
      needs_snapshot = !@deletion.delete_backups && snapshot_wanted?
      raise Cancel, "no backup storage to keep a final snapshot in (finish setup's storage step, or delete the backups too)" if needs_snapshot && !@project.backup_location

      if needs_snapshot
        step("snapshot")
        final_snapshot
      end
      write(removing_at: Time.current)
    end

    def check_docker
      ran = docker("version", "--format", "{{.Server.Version}}")
      raise Cancel, "Docker didn't answer: #{tail(ran.output)}" unless ran.success
      say("ok  Docker #{ran.output.strip}")
    end

    def cloudflare? = installation.connected? && installation.cloudflare_api_token.present?

    def check_cloudflare
      client.get("/zones/#{installation.cloudflare_zone_id}/dns_records", per_page: 1)
      say("ok  Cloudflare answers")
    rescue Cloudflare::Error => e
      raise Cancel, "Cloudflare said no: #{e.message}"
    end

    def snapshot_wanted? = @project.running_deploy.present? && (@project.volumes.any? || @project.databases.any?)

    # A backup of kind final: retention forgets by kind, so never this one.
    def final_snapshot
      run = @project.backup_runs.create!(location: @project.backup_location, kind: "final", reason: "delete", status: "queued", heartbeat_at: Time.current)
      token = BackupRun.claim!(run)
      raise Cancel, "a backup of #{@project.name} is running; delete once it's done" if token == :busy
      raise Cancel, "the final snapshot couldn't start" unless token

      Backup.new(run, token).call
      run.reload
      raise Cancel, "the final snapshot failed: #{run.error}" unless run.status.in?(%w[go skipped])
      if run.status == "go"
        write(snapshot_id: run.snapshot_id, snapshot_location_id: run.location_id)
        say("ok  final snapshot #{run.snapshot_id.first(8)} in #{run.location.name}")
      end
    end

    # ── Removal ───────────────────────────────────────────────────────────

    def remove
      REMOVAL_STEPS.each do |name|
        step(name)
        send(:"remove_#{name}")
      end
    end

    # Maintenance routes: the tunnel's rules leave out projects being
    # removed, so their hosts stop reaching Mission Control.
    def remove_routes
      return say("ok  not in maintenance") unless @project.maintenance? && cloudflare? && installation.tunnel_id.present?

      File.open(Maintenance::LOCK, File::RDWR | File::CREAT, 0o600) do |lock|
        lock.flock(File::LOCK_EX)
        TunnelRoutes.push!(installation)
      end
      say("ok  #{@project.name}'s hosts no longer reach Mission Control")
    rescue Cloudflare::Error => e
      raise Stop, "Cloudflare said no: #{e.message}"
    end

    # Records commented exactly managed-by:houston project:<name>, in the
    # zones the project's hostnames live in.
    def remove_dns
      return say("ok  Cloudflare isn't connected") unless cloudflare?

      comment = "#{Cloudflare::Records::MANAGED} project:#{@project.name}"
      zones.each do |zone|
        client.get("/zones/#{zone}/dns_records", "comment.exact" => comment, per_page: 5000).each do |record|
          next unless record["comment"] == comment
          client.delete("/zones/#{zone}/dns_records/#{record["id"]}")
          say("ok  removed #{record["name"]}")
        end
      end
    rescue Cloudflare::Error => e
      raise Stop, "Cloudflare said no: #{e.message}"
    end

    def zones
      domains = (@project.domains.to_a + @project.domain_states.to_h.keys).uniq.reject { |d| d.end_with?(".#{installation.base_domain}") }
      ([ installation.cloudflare_zone_id ] + domains.filter_map { |d| find_zone(d) }).compact_blank.uniq
    end

    def find_zone(domain)
      labels = domain.split(".")
      (0...(labels.size - 1)).each do |i|
        zone = client.get("/zones", name: labels[i..].join(".")).first
        return zone["id"] if zone
      end
      nil
    end

    def remove_containers
      name = @project.name
      remove_ids(ids("ps", "-aq", "--filter", "label=service=#{name}"))
      remove_ids(ids("ps", "-aq", "--filter", "name=^/#{name}-release-[0-9a-f]+$"))
      theirs = ProjectHost.where.not(project_id: @project.id).pluck(:name)
      ((@project.hosts.pluck(:name) - [ name ]).sort + [ "houston-kamal-#{name}" ] - theirs).each do |container|
        ran = docker("rm", "-f", container)
        raise Stop, "couldn't remove #{container}: #{tail(ran.output)}" unless ran.success || ran.output.include?("No such container")
      end
      ran = docker("exec", "kamal-proxy", "kamal-proxy", "remove", "#{name}-web")
      raise Stop, "kamal-proxy kept #{name}-web: #{tail(ran.output)}" unless ran.success || ran.output.include?("service not found") || ran.output.include?("No such container: kamal-proxy")
      say("ok  removed #{name}'s containers and its route in kamal-proxy")
    end

    def remove_ids(list)
      return if list.empty?
      ran = docker("rm", "-f", *list)
      raise Stop, "couldn't remove containers #{list.join(", ")}: #{tail(ran.output)}" unless ran.success
    end

    def remove_volumes
      name = Regexp.escape(@project.name)
      ours = /\A(#{name}(\.g\d+)?_[^\s\/]+|houston-(backup|restore)\.#{name})\z/
      volumes = ids("volume", "ls", "-q").grep(ours)
      volumes.each { |v| remove_ids(ids("ps", "-aq", "--filter", "volume=#{v}")) }
      volumes.each do |v|
        ran = docker("volume", "rm", v)
        raise Stop, "couldn't remove #{v}: #{tail(ran.output)}" unless ran.success || ran.output.include?("no such volume")
      end
      say("ok  removed #{volumes.size} #{"volume".pluralize(volumes.size)}")
    end

    # Each storage location that held one of its volumes: the folders there.
    def remove_folders
      StorageLocation.where(id: @project.project_volumes.where.not(location_id: nil).select(:location_id), kind: %w[nfs local]).order(:name).each do |location|
        root = location.settings["path"]
        if location.kind == "nfs"
          ensured = location.ensure_volume
          raise Stop, "couldn't reach #{location.name}: #{tail(ensured.output)}" unless ensured.success
          root = location.volume_name
        end
        ran = docker("run", "--rm", "--user", "0", "-v", "#{root}:/location", "--entrypoint", "sh", tools, "-c", REMOVE_FOLDERS, "sh", @project.name)
        raise Stop, "couldn't remove #{@project.name}'s folders on #{location.name}: #{tail(ran.output)}" unless ran.success
        say("ok  removed volumes/#{@project.name} on #{location.name}")
      end
    end

    def remove_images
      images = ids("images", "-q", "--filter", "reference=127.0.0.1:5000/#{@project.name}:*").uniq
      if images.any?
        ran = docker("rmi", "-f", *images)
        raise Stop, "couldn't remove #{@project.name}'s images: #{tail(ran.output)}" unless ran.success
      end
      say("ok  removed #{images.size} #{"image".pluralize(images.size)} from this server")
    end

    # Its manifests in Houston's registry; the space is freed afterwards by
    # RegistryCleanupJob, once nothing is being pushed.
    def remove_registry
      deleted = Registry.new.delete_repository(@project.name)
      say("ok  deleted #{deleted} #{"image".pluralize(deleted)} from the registry")
    rescue Registry::Error => e
      raise Stop, e.message
    end

    def remove_files
      ran = docker("run", "--rm", "--user", "0", "-v", "#{kamal_home}:/kamal", "-v", "#{RUNNERS_DIR}:/runners",
                   "--entrypoint", "sh", tools, "-c", REMOVE_FILES, "sh", @project.name)
      raise Stop, "couldn't remove Kamal's files and the runners' checkouts: #{tail(ran.output)}" unless ran.success
      say("ok  removed Kamal's files (its env files held secrets) and the runners' checkouts")
    end

    def remove_backups
      return say("ok  backups kept#{" (final snapshot #{@deletion.snapshot_id.first(8)})" if @deletion.snapshot_id}") unless @deletion.delete_backups

      Snapshots.locations_for(@project).each do |location|
        ran = DockerCommand.run(*location.restic_args("snapshots", "--json", "--host", "houston", "--tag", "project:#{@project.name}"), env: location.restic_env, timeout: RESTIC_TIMEOUT)
        raise Stop, "couldn't list #{location.name}'s snapshots: #{tail(ran.output)}" unless ran.success
        ids = JSON.parse(ran.output).map { |s| s["id"] }.grep(/\A\h{64}\z/)
        if ids.any?
          forgot = DockerCommand.run(*location.restic_args("forget", "--retry-lock", "30m", "--prune", *ids), env: location.restic_env, timeout: RESTIC_TIMEOUT)
          raise Stop, "couldn't delete #{location.name}'s snapshots: #{tail(forgot.output)}" unless forgot.success
        end
        Snapshots.forget_cache(@project, location)
        say("ok  deleted #{ids.size} #{"snapshot".pluralize(ids.size)} in #{location.name}")
      end
    rescue JSON::ParserError
      raise Stop, "restic's list wasn't JSON"
    end

    def remove_rows
      StorageLocation.find_each { |location| Snapshots.forget_cache(@project, location) }
      @project.destroy!
      say("ok  removed #{@project.name} from Mission Control")
    end

    # ── Helpers ───────────────────────────────────────────────────────────

    def client = @client ||= Cloudflare::Client.new(installation.cloudflare_api_token)

    def kamal_home = ENV["HOUSTON_KAMAL_HOME"].presence

    def tools = ENV.fetch("HOUSTON_TOOLS_IMAGE", "houston/mission-control:local")

    def docker(*args)
      say("$ docker #{args.first(5).join(" ")}")
      DockerCommand.run(*args, timeout: TIMEOUT)
    end

    def ids(*args)
      ran = docker(*args)
      raise Stop, "docker #{args.first(2).join(" ")} failed: #{tail(ran.output)}" unless ran.success
      ran.output.split
    end

    def tail(output) = output.to_s.lines.last(5).join.strip.truncate(1000)

    def with_heartbeat
      beat = Thread.new do
        loop do
          sleep HEARTBEAT_EVERY
          ActiveRecord::Base.connection_pool.with_connection { ours.update_all(heartbeat_at: Time.current) }
        end
      end
      yield
    ensure
      beat&.kill&.join
    end
end
