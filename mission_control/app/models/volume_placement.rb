# Makes a project's named volumes where they were chosen to live, at sync,
# before Kamal mounts them: a plain Docker volume on local disk, or one with
# driver options pointing into its location (a directory per project and
# volume). A volume that exists already is checked, never moved.
class VolumePlacement
  class Refused < StandardError; end

  # generation: whose volumes to make (a restore makes the next one's).
  # volumes: which ([{ "name", "path" }]); a restore's are its own commit's.
  def initialize(project, generation: project.data_generation, volumes: project.volumes)
    @project = project
    @generation = Generation.new(project, generation)
    @volumes = volumes
  end

  def place!
    @volumes.each do |v|
      volume = @project.project_volumes.create_or_find_by!(name: v["name"]) # the unique index settles two syncs at once
      docker_name = @generation.volume(volume.name)
      want = options(volume)
      inspected = DockerCommand.run("volume", "inspect", "--format", "{{json .Options}}", docker_name)
      if inspected.success
        have = JSON.parse(inspected.output.presence || "null") || {}
        unless have.slice("type", "device") == want.slice("type", "device")
          raise Refused, "#{docker_name} already exists on #{describe(have)}, not #{volume.where_words}; " \
                         "Houston doesn't move volumes yet: choose #{describe(have)}, or move it yourself"
        end
      else
        make_directory(volume) if volume.location
        created = DockerCommand.run("volume", "create", *want.flat_map { |k, value| [ "--opt", "#{k}=#{value}" ] }.then { |opts| opts.any? ? [ "--driver", "local", *opts ] : [] }, docker_name)
        raise Refused, "couldn't create #{docker_name}: #{created.output.strip}" unless created.success
      end
      volume.update!(placed_at: Time.current) unless volume.placed_at
    end
  end

  private
    # docker volume create's options for where the volume lives: none for
    # local disk.
    def options(volume)
      location = volume.location or return {}
      case location.kind
      when "nfs"
        { "type" => "nfs", "o" => "addr=#{location.settings["server"]},rw,nfsvers=4", "device" => ":#{location.settings["export"]}/#{subdirectory(volume)}" }
      when "local"
        { "type" => "none", "o" => "bind", "device" => "#{location.settings["path"]}/#{subdirectory(volume)}" }
      end
    end

    def subdirectory(volume) = @generation.directory(volume.name)

    # The directory must exist before a volume can point into it.
    def make_directory(volume)
      location = volume.location
      root = location.settings["path"]
      if location.kind == "nfs"
        # docker run -v with a missing named volume would make an empty local one.
        ensured = location.ensure_volume
        raise Refused, "couldn't reach #{location.name}'s NFS volume: #{ensured.output.strip}" unless ensured.success
        root = location.volume_name
      end
      made = DockerCommand.run("run", "--rm", "--user", "0", "-v", "#{root}:/location", "--entrypoint", "mkdir", tools, "-p", "/location/#{subdirectory(volume)}")
      raise Refused, "couldn't make #{@generation.volume(volume.name)}'s directory on #{location.name}: #{made.output.strip}" unless made.success
    end

    def describe(options)
      return "local disk" if options["device"].blank?
      location = StorageLocation.where(kind: %w[nfs local]).find { |l| options["device"].start_with?(l.kind == "nfs" ? ":#{l.settings["export"]}/" : "#{l.settings["path"]}/") }
      location&.name || options["device"]
    end

    def tools = ENV.fetch("HOUSTON_TOOLS_IMAGE", "houston/mission-control:local")
end
