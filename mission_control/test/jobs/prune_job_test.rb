require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/backup_helpers"

class PruneJobTest < ActiveJob::TestCase
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers

  test "pruning each location" do
    unas = storage_locations(:unas)
    offsite = StorageLocation.create!(name: "b2-offsite", kind: "b2", settings: { "bucket" => "sm" }, credentials: { "key_id" => "k", "application_key" => "secret-app-key" },
                                      restic_password: "offsite-pw", verified_at: Time.current, acknowledged_at: Time.current)
    StorageLocation.create!(name: "unused", kind: "local", settings: { "path" => "/srv/b" }, restic_password: "pw", verified_at: Time.current, acknowledged_at: Time.current)
    StorageLocation.create!(name: "unfinished", kind: "local", settings: { "path" => "/srv/c" }, restic_password: "pw", verified_at: Time.current)
    project = make_backup_project
    [ unas, offsite ].each { |location| BackupRun.create!(project:, location:, kind: "auto", reason: "manual", status: "go", heartbeat_at: Time.current) }

    fake = FakeDocker.new do |args, env|
      failure("Fatal: unable to open config file: Stat: 403 Forbidden\n") if env["RESTIC_PASSWORD"] == "offsite-pw"
    end
    logged = capture_log { use_fake_docker(fake) { PruneJob.perform_now } }

    prunes = fake.calls.select { |c| c.args.include?("prune") }
    assert_equal [ "restic-password-xyz", "offsite-pw" ].sort, prunes.map { |c| c.env["RESTIC_PASSWORD"] }.sort, "each used, acknowledged location once"
    assert prunes.all? { |c| c.args.last(3) == [ "prune", "--retry-lock", "30m" ] && c.timeout == 3.hours.to_i }, prunes.map(&:args).inspect
    assert unas.reload.pruned_at
    assert_nil unas.prune_error
    assert_match "403 Forbidden", offsite.reload.prune_error
    assert_match "prune of b2-offsite failed", logged
    assert_not_includes fake.all_args.join(" "), "secret-app-key"

    assert_equal "backups", PruneJob.new.queue_name
    recurring = YAML.load(ERB.new(Rails.root.join("config/recurring.yml").read).result, aliases: true)
    assert_equal({ "class" => "PruneJob", "schedule" => "every day at 4:30am UTC" }, recurring.dig("production", "prune_backups"))
  end

  private
    def capture_log
      io = StringIO.new
      original = Rails.logger
      Rails.logger = ActiveSupport::Logger.new(io)
      yield
      io.string
    ensure
      Rails.logger = original
    end
end
