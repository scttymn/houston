# A project's snapshots, read from restic (Houston keeps no snapshot table):
# newest first, cached for a while because listing a remote repository can
# take seconds. A GO backup clears the cache.
class Snapshots
  class Unavailable < StandardError; end

  Snapshot = Data.define(:id, :short_id, :time, :kind, :reason, :deploy, :sha, :bytes, :location) do
    def initialize(location: nil, **rest) = super
  end

  CACHE_FOR = 10.minutes
  LIMIT = 1000
  LIST_TIMEOUT = 120

  def self.for(project, location)
    key = cache_key(project, location)
    cached = Rails.cache.read(key)
    return cached if cached

    list(project, location).tap { |snapshots| Rails.cache.write(key, snapshots, expires_in: CACHE_FOR) }
  end

  # Every location the project's backups used, and its current target.
  def self.locations_for(project)
    used = StorageLocation.where(id: project.backup_runs.where(operation: "backup").select(:location_id)).where.not(acknowledged_at: nil).to_a
    (used + [ project.backup_location ].compact).uniq.sort_by(&:name)
  end

  # The project's snapshots in all of them, newest first, each saying where it is.
  def self.across(project)
    locations_for(project).flat_map { |location| self.for(project, location).map { |s| s.with(location:) } }.sort_by(&:time).reverse
  end

  def self.forget_cache(project, location) = Rails.cache.delete(cache_key(project, location))

  def self.cache_key(project, location) = [ "snapshots", project.name, location.id ]

  def self.list(project, location)
    ran = DockerCommand.run(*location.restic_args("snapshots", "--json", "--host", "houston", "--tag", "project:#{project.name}"),
                            env: location.restic_env, timeout: LIST_TIMEOUT)
    raise Unavailable, ran.output.to_s.lines.last(5).join.strip.truncate(500) unless ran.success

    entries = JSON.parse(ran.output)
    raise Unavailable, "restic's list wasn't a list" unless entries.is_a?(Array)
    entries.map { |entry| snapshot(entry) }.sort_by(&:time).reverse.first(LIMIT)
  rescue JSON::ParserError
    raise Unavailable, "restic's list wasn't JSON: #{ran.output.to_s.truncate(200)}"
  end

  def self.snapshot(entry)
    tags = entry["tags"].to_a.to_h { |tag| tag.split(":", 2) }
    Snapshot.new(id: entry["id"], short_id: entry["short_id"] || entry["id"].to_s.first(8), time: Time.iso8601(entry["time"]).utc,
                 kind: tags["kind"], reason: tags["reason"], deploy: tags["deploy"]&.to_i, sha: tags["sha"],
                 bytes: entry.dig("summary", "total_bytes_processed"))
  end
  private_class_method :list, :snapshot, :cache_key
end
