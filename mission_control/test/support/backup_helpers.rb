# A project with data to back up, and docker answering like the server does.
module BackupHelpers
  SNAPSHOT = "5c5edd4c" + "0" * 56
  TOOLS = "houston/mission-control:test"

  def self.included(base)
    base.setup do
      ENV["HOUSTON_TOOLS_IMAGE"] = TOOLS
      storage_locations(:unas).update!(restic_password: "restic-password-xyz")
    end
    base.teardown { ENV.delete("HOUSTON_TOOLS_IMAGE") }
  end

  def hex(s) = s.unpack1("H*")

  # equip: a storage volume with one SQLite file, and Postgres (db) with two
  # databases, one of them with a hostile name.
  def make_backup_project
    make_project("equip", services: %w[app db cache]).tap do |project|
      project.update!(volumes: [ { "name" => "storage", "path" => "/rails/storage" } ],
                      databases: [ { "service" => "db", "image" => "postgres:17" } ])
      make_deploy(project, 1, "go", sha: "a" * 40)
    end
  end

  def summary_line(id = SNAPSHOT, bytes = 410_000_000)
    { message_type: "summary", snapshot_id: id, total_bytes_processed: bytes }.to_json
  end

  # Answers each step the way a healthy server would. overrides: {matcher =>
  # Result, or a proc called when the command runs}; a matcher is a Regexp
  # on the joined args or a proc on the args.
  def backup_docker(overrides = {})
    FakeDocker.new do |args, _env|
      joined = args.join(" ")
      hit = overrides.find { |matcher, _| matcher.is_a?(Regexp) ? joined.match?(matcher) : matcher.call(args) }
      next(hit.last.respond_to?(:call) ? hit.last.call : hit.last) if hit

      if args[0] == "exec" && joined.include?("pg_database")
        DockerCommand::Result.new(success: true, output: "#{hex("equip_production")}\n#{hex("we'ird=db")}\n")
      elsif args.include?("/rails/lib/backup/sqlite.rb")
        DockerCommand::Result.new(success: true, output: { sqlite: [ { volume: "storage", path: "production.sqlite3", file: "sqlite/1.sqlite3" } ], warnings: [], errors: [] }.to_json + "\n")
      elsif args.include?(StorageLocation::RESTIC_IMAGE)
        DockerCommand::Result.new(success: true, output: "{\"message_type\":\"status\",\"percent_done\":1}\n#{summary_line}\n")
      end
    end
  end

  def restic_backups(fake) = fake.calls.select { |c| c.args.include?(StorageLocation::RESTIC_IMAGE) && c.args.include?("backup") }
end
