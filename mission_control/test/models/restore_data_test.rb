require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/backup_helpers"

# A restore's data engine: a snapshot's data into generation g+1, beside the
# serving generation g (docs/plans/restore.md, Batch 4).
class RestoreDataTest < ActiveSupport::TestCase
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers

  SHA = "a" * 40
  STAGING = "houston-restore.equip"
  FEED = [ "run", "--rm", "--name", "houston-restore.equip.feed", "--user", "0", "-v", "#{STAGING}:/restore:ro",
           "--entrypoint", "cat", BackupHelpers::TOOLS ].freeze

  setup do
    @project = make_backup_project # generation 1, deploy #1 of SHA serving
    @restore, _token, = Deploy.start!(@project, sha: SHA, ref: "refs/heads/main")
    @restore.update!(kind: "restore", source_snapshot_id: SNAPSHOT, source_location: storage_locations(:unas), generation: 2)
    @run = BackupRun.request_restore!(@restore)
    @token = BackupRun.claim!(@run)
  end

  def manifest(**changes)
    { version: 1, project: "equip", sha: SHA, volumes: [ { name: "storage", path: "/rails/storage" } ],
      postgres: [ { service: "db", image: "postgres:17", globals: "postgres/db/globals.sql",
                    databases: [ { name: "postgres", file: "postgres/db/1.dump" }, { name: "we'ird=db", file: "postgres/db/2.dump" } ] } ],
      sqlite: [ { volume: "storage", path: "production.sqlite3", file: "sqlite/1.sqlite3" } ] }.merge(changes).to_json
  end

  # Docker answering a healthy restore; overrides as in backup_docker.
  def restore_docker(manifest_json = manifest, overrides = {})
    FakeDocker.new do |args, _env|
      joined = args.join(" ")
      hit = overrides.find { |matcher, _| matcher.is_a?(Regexp) ? joined.match?(matcher) : matcher.call(args) }
      next(hit.last.respond_to?(:call) ? hit.last.call : hit.last) if hit

      if args[0..2] == [ "volume", "inspect", "--format" ] then failure("no such volume\n")
      elsif args.last == "/restore/out/houston.json" then DockerCommand::Result.new(success: true, output: manifest_json)
      end
    end
  end

  def restore(fake)
    use_fake_docker(fake) { RestoreData.new(@run, @token).call }
    @run.reload
  end

  test "a restore, step by step" do
    storage_locations(:unas).update!(restic_password: "restic-password-xyz")
    fake = restore_docker
    restore(fake)
    calls = fake.calls.map(&:args)

    # Generation 2's app volume is made first (placement, idempotent).
    assert_equal [ "volume", "inspect", "--format", "{{json .Options}}", "equip.g2_storage" ], calls[0]
    assert_equal [ "volume", "create", "equip.g2_storage" ], calls[1]
    assert_equal [ "rm", "-f", "houston-restore.equip.restic", "houston-restore.equip.read", "houston-restore.equip.fill", "houston-restore.equip.feed" ], calls[2]
    assert_equal [ [ "volume", "rm", "-f", STAGING ], [ "volume", "create", STAGING ] ], calls[3..4]

    restic = calls[5]
    assert_equal [ StorageLocation::RESTIC_IMAGE, "restore", SNAPSHOT, "--target", "/restore" ], restic.drop(restic.index(StorageLocation::RESTIC_IMAGE))
    assert_includes restic.each_cons(2).to_a, [ "-v", "#{STAGING}:/restore" ]
    assert_includes restic.each_cons(2).to_a, [ "-v", "houston-restic-cache:/root/.cache/restic" ]
    assert_equal "restic-password-xyz", fake.calls[5].env["RESTIC_PASSWORD"]
    assert_not fake.all_args.any? { |a| a.include?("restic-password-xyz") }

    assert_equal [ "run", "--rm", "--name", "houston-restore.equip.read", "--user", "0", "-v", "#{STAGING}:/restore:ro",
                   "--entrypoint", "cat", BackupHelpers::TOOLS, "/restore/out/houston.json" ], calls[6]

    # The volume: emptied, refilled, SQLite put in place (names and paths as argv).
    fill = calls[7]
    assert_equal [ "run", "--rm", "--name", "houston-restore.equip.fill", "--user", "0", "-v", "equip.g2_storage:/v", "-v", "#{STAGING}:/restore:ro",
                   "--entrypoint", "sh", BackupHelpers::TOOLS, "-c", RestoreData::FILL, "sh", "storage", "sqlite/1.sqlite3", "production.sqlite3" ], fill
    assert_match "find /v -mindepth 1 -delete", RestoreData::FILL
    assert_match(/rm -f "\/v\/\$2-wal" "\/v\/\$2-shm" "\/v\/\$2-journal"/, RestoreData::FILL)

    # Postgres, into generation 2's container.
    pg = "equip-db-g2"
    assert_equal [ "exec", pg, "sh", "-c", RestoreData::PG_READY ], calls[8]
    assert_equal FEED + [ "/restore/out/postgres/db/globals.sql", "|", "exec", "-i", pg, "sh", "-c", RestoreData::PG_ROLES ], calls[9]
    assert_equal [ "exec", pg, "sh", "-c", RestoreData::PG_KEEP_PASSWORD ], calls[10]
    assert_equal [ "exec", pg, "sh", "-c", RestoreData::PG_RECREATE, "sh", "postgres" ], calls[11]
    assert_equal FEED + [ "/restore/out/postgres/db/1.dump", "|", "exec", "-i", pg, "sh", "-c", RestoreData::PG_RESTORE, "sh", "dbname='postgres'" ], calls[12]
    assert_equal [ "exec", pg, "sh", "-c", RestoreData::PG_RECREATE, "sh", "we'ird=db" ], calls[13]
    assert_equal FEED + [ "/restore/out/postgres/db/2.dump", "|", "exec", "-i", pg, "sh", "-c", RestoreData::PG_RESTORE, "sh", "dbname='we\\'ird=db'" ], calls[14]
    assert_match "dropdb", RestoreData::PG_RECREATE
    assert_match "--force --if-exists --maintenance-db=template1 -- \"$1\"", RestoreData::PG_RECREATE
    assert_match "--no-owner", RestoreData::PG_RESTORE
    assert_match ":'pw'", RestoreData::PG_KEEP_PASSWORD

    assert_equal [ "volume", "rm", "-f", STAGING ], calls.last
    assert_equal 16, calls.size

    assert_equal [ "go", SNAPSHOT ], [ @run.status, @run.source_snapshot_id ]
    assert_equal({ "volumes" => [ "storage" ], "sqlite" => [ { "volume" => "storage", "path" => "production.sqlite3" } ],
                   "databases" => [ { "service" => "db", "name" => "postgres" }, { "service" => "db", "name" => "we'ird=db" } ] }, @run.found)
  end

  test "the manifest is checked before anything is touched" do
    {
      "no manifest" => [ nil, "couldn't read the snapshot's manifest" ],
      "not JSON" => [ "{not json", "the snapshot's manifest isn't JSON" ],
      "another project" => [ manifest(project: "other"), "the snapshot is of other, not equip" ],
      "another commit" => [ manifest(sha: "b" * 40), "the snapshot was taken at bbbbbbb, not the restore's aaaaaaa" ],
      "a file outside Houston's form" => [ manifest(sqlite: [ { volume: "storage", path: "x", file: "../../etc/passwd" } ]), "not one Houston writes" ],
      "a path that climbs out" => [ manifest(sqlite: [ { volume: "storage", path: "../../x", file: "sqlite/1.sqlite3" } ]), "leaves its volume" ],
      "an unknown volume" => [ manifest(volumes: [ { name: "media", path: "/media" } ]), "equip has no volume media" ]
    }.each do |name, (json, message)|
      BackupRun.where(operation: "restore").delete_all # one per restore deploy: each case is a fresh one
      @run = BackupRun.request_restore!(@restore)
      @token = BackupRun.claim!(@run)
      fake = json ? restore_docker(json) : restore_docker(manifest, ->(args) { args.last == "/restore/out/houston.json" } => failure("cat: can't open\n"))
      restore(fake)
      assert_equal "no_go", @run.status, name
      assert_match message, @run.error, name
      touched = fake.calls.map { |c| c.args.join(" ") }.grep(/find \/v|dropdb|pg_restore|RestoreData/)
      assert_empty fake.calls.select { |c| c.args.include?(RestoreData::FILL) || c.args.include?(RestoreData::PG_RECREATE) }, "#{name}: something was touched: #{touched}"
      assert_equal [ "volume", "rm", "-f", STAGING ], fake.calls.last.args, name
    end
  end

  # Defense in depth: a restore never writes into the generation that's
  # serving, whatever its deploy row says.
  test "never into the serving generation" do
    @restore.update!(generation: 1)
    fake = restore_docker
    restore(fake)
    assert_equal "no_go", @run.status
    assert_match "generation 1 is the one serving", @run.error
    assert_empty fake.calls.select { |c| c.args.include?(RestoreData::FILL) || c.args.include?(RestoreData::PG_RECREATE) || c.args[0..1] == %w[volume create] }
  end

  test "a failing step stops the restore and cleans up" do
    {
      "restic" => [ ->(args) { args.include?(StorageLocation::RESTIC_IMAGE) }, failure("Fatal: no matching ID found for prefix\n"), "restic restore failed" ],
      "the copy" => [ ->(args) { args.include?(RestoreData::FILL) }, failure("cp: write error: No space left on device\n"), "couldn't restore the volume storage" ],
      "pg_restore" => [ /pg_restore .*ird=db/, failure("pg_restore: error: could not execute query\n"), %(pg_restore of db's database "we'ird=db" failed) ]
    }.each do |name, (matcher, result, message)|
      BackupRun.where(operation: "restore").delete_all # one per restore deploy: each case is a fresh one
      @run = BackupRun.request_restore!(@restore)
      @token = BackupRun.claim!(@run)
      fake = restore_docker(manifest, matcher => result)
      restore(fake)
      assert_equal "no_go", @run.status, name
      assert_match message, @run.error, name
      assert_equal [ "volume", "rm", "-f", STAGING ], fake.calls.last.args, name
    end
  end
end
