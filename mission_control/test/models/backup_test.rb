require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/backup_helpers"

class BackupTest < ActiveSupport::TestCase
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers

  # Helpers run as root: the staging volume is root's, and the app's volumes
  # belong to whoever the app runs as (Mission Control's image is uid 1000).
  WRITE = [ "run", "--rm", "-i", "--name", "houston-backup.equip.write", "--user", "0", "-v", "houston-backup.equip:/out",
            "--entrypoint", "sh", BackupHelpers::TOOLS, "-c", 'mkdir -p "$(dirname "$1")" && cat > "$1"', "sh" ].freeze

  setup do
    @project = make_backup_project
    @run = BackupRun.request!(@project)
    @token = BackupRun.claim!(@run)
  end

  def back_up(fake)
    use_fake_docker(fake) { Backup.new(@run, @token).call }
    @run.reload
  end

  test "a backup, step by step" do
    fake = backup_docker
    back_up(fake)
    calls = fake.calls.map(&:args)

    assert_equal [ "rm", "-f", "houston-backup.equip.sqlite", "houston-backup.equip.restic", "houston-backup.equip.write" ], calls[0]
    assert_equal [ "volume", "rm", "-f", "houston-backup.equip" ], calls[1]
    assert_equal [ "volume", "create", "houston-backup.equip" ], calls[2]

    # Postgres: list the databases, dump each (the name travels as argv, in
    # a conninfo with quotes escaped), then the roles.
    assert_equal [ "exec", "equip-db", "sh", "-c" ], calls[3].first(4)
    assert_match(/pg_database.*datistemplate/, calls[3][4])
    dump = [ "exec", "equip-db", "sh", "-c", 'pg_dump -U "${POSTGRES_USER:-postgres}" -Fc -d "$1"', "sh" ]
    assert_equal dump + [ "dbname='equip_production'", "|" ] + WRITE + [ "/out/postgres/db/1.dump" ], calls[4]
    assert_equal dump + [ "dbname='we\\'ird=db'", "|" ] + WRITE + [ "/out/postgres/db/2.dump" ], calls[5]
    assert_equal [ "exec", "equip-db", "sh", "-c", 'pg_dumpall -U "${POSTGRES_USER:-postgres}" --globals-only', "|" ] + WRITE + [ "/out/postgres/db/globals.sql" ], calls[6]

    # SQLite: the helper (lib/backup/sqlite.rb) mounts each app volume (read-write: SQLite may need
    # its -shm) and the staging volume.
    assert_equal [ "run", "--rm", "--name", "houston-backup.equip.sqlite", "--user", "0", "-v", "equip_storage:/data/storage", "-v", "houston-backup.equip:/out",
                   "--entrypoint", "ruby", BackupHelpers::TOOLS, "/rails/lib/backup/sqlite.rb", "/data", "/out" ], calls[7]

    # The manifest, from stdin.
    assert_equal WRITE + [ "/out/houston.json" ], calls[8]
    manifest = JSON.parse(fake.calls[8].stdin)
    assert_equal({ "project" => "equip", "sha" => "a" * 40, "volumes" => [ { "name" => "storage", "path" => "/rails/storage" } ],
                   "postgres" => [ { "service" => "db", "image" => "postgres:17", "globals" => "postgres/db/globals.sql",
                                     "databases" => [ { "name" => "equip_production", "file" => "postgres/db/1.dump" }, { "name" => "we'ird=db", "file" => "postgres/db/2.dump" } ] } ],
                   "sqlite" => [ { "volume" => "storage", "path" => "production.sqlite3", "file" => "sqlite/1.sqlite3" } ] }, manifest.except("version"))

    restic = calls[9]
    assert_equal [ "run", "--rm", "--name", "houston-backup.equip.restic" ], restic.first(4)
    assert_includes restic.each_cons(2).to_a, [ "-v", "houston-restic-cache:/root/.cache/restic" ]
    assert_includes restic.each_cons(2).to_a, [ "-v", "houston-storage-unas-nfs:/repo" ]
    assert_includes restic.each_cons(2).to_a, [ "-v", "equip_storage:/data/storage:ro" ]
    assert_includes restic.each_cons(2).to_a, [ "-v", "houston-backup.equip:/out:ro" ]
    assert_equal [ StorageLocation::RESTIC_IMAGE, "backup", "--host", "houston", "--json", "--tag", "project:equip", "--tag", "sha:#{"a" * 40}",
                   "--tag", "kind:auto", "--tag", "reason:manual", "--exclude-file", "/out/.houston/exclude", "/data", "/out" ],
                 restic.drop(restic.index(StorageLocation::RESTIC_IMAGE))
    assert_equal "restic-password-xyz", fake.calls[9].env["RESTIC_PASSWORD"]

    assert_equal "forget", calls[10][calls[10].index(StorageLocation::RESTIC_IMAGE) + 1]
    assert_equal [ "volume", "rm", "-f", "houston-backup.equip" ], calls[11]
    assert_equal 12, calls.size

    # Secrets only in the docker process's environment.
    assert_not fake.all_args.any? { |a| a.include?("restic-password-xyz") }, "the restic password is in argv"
    assert_not_includes @run.log.to_s, "restic-password-xyz"

    assert_equal [ "go", SNAPSHOT, 410_000_000, nil ], [ @run.status, @run.snapshot_id, @run.bytes, @run.error ]
    assert_equal({ "databases" => [ { "service" => "db", "name" => "equip_production" }, { "service" => "db", "name" => "we'ird=db" } ],
                   "sqlite" => [ { "volume" => "storage", "path" => "production.sqlite3" } ] }, @run.found)
    assert @run.finished_at
    assert_equal "a" * 40, @run.sha
  end

  test "a failing step stops the backup and cleans up" do
    fake = backup_docker(/pg_dump .*ird=db/ => failure("pg_dump: error: connection to server failed\n"))
    back_up(fake)
    assert_equal "no_go", @run.status
    assert_match(/pg_dump of db's database "we'ird=db" failed/, @run.error)
    assert_match "connection to server failed", @run.error
    assert_empty restic_backups(fake)
    assert_equal [ "volume", "rm", "-f", "houston-backup.equip" ], fake.calls.last.args

    @run = BackupRun.request!(@project)
    @token = BackupRun.claim!(@run)
    fake = backup_docker(->(args) { args.include?(StorageLocation::RESTIC_IMAGE) } => failure("Fatal: unable to open repository at /repo: permission denied\n"))
    back_up(fake)
    assert_equal "no_go", @run.status
    assert_match "restic backup failed", @run.error
    assert_match "unable to open repository", @run.error
    assert_equal [ "volume", "rm", "-f", "houston-backup.equip" ], fake.calls.last.args

    # Exit 3: saved, but some files couldn't be read. GO, with the warning.
    @run = BackupRun.request!(@project)
    @token = BackupRun.claim!(@run)
    unreadable = { message_type: "error", error: { message: "open /data/storage/tmp/x: no such file or directory" }, during: "archival", item: "/data/storage/tmp/x" }.to_json
    fake = backup_docker(->(args) { args.include?(StorageLocation::RESTIC_IMAGE) } =>
                           failure("#{unreadable}\n#{summary_line}\nWarning: at least one source file could not be read\n", code: 3))
    back_up(fake)
    assert_equal [ "go", SNAPSHOT ], [ @run.status, @run.snapshot_id ]
    assert_match "1 file couldn't be read: /data/storage/tmp/x", @run.error
  end

  test "retention after a backup" do
    Rails.cache.write([ "snapshots", "equip", storage_locations(:unas).id ], [ :stale ])
    fake = backup_docker
    back_up(fake)
    forgets = restic_forgets(fake)
    assert_equal 1, forgets.size
    assert_equal [ StorageLocation::RESTIC_IMAGE, "forget", "--host", "houston", "--tag", "project:equip,kind:auto", "--group-by", "", "--json", "--keep-daily", "14" ],
                 forgets.first.args.drop(forgets.first.args.index(StorageLocation::RESTIC_IMAGE))
    assert_equal "go", @run.status
    assert_nil Rails.cache.read([ "snapshots", "equip", storage_locations(:unas).id ]), "a GO backup clears the snapshots cache"
    assert_equal [ "volume", "rm", "-f", "houston-backup.equip" ], fake.calls.last.args

    # A deploy-kind run keeps the last N.
    @project.update!(keep_deploy: 4)
    @run = BackupRun.create!(project: @project, location: storage_locations(:unas), kind: "deploy", reason: "deploy", deploy_number: 7, status: "queued", heartbeat_at: Time.current)
    @token = BackupRun.claim!(@run)
    fake = backup_docker
    back_up(fake)
    assert_equal [ "--tag", "project:equip,kind:deploy", "--group-by", "", "--json", "--keep-last", "4" ], restic_forgets(fake).first.args.last(7)
    assert_includes restic_backups(fake).first.args.each_cons(2).to_a, [ "--tag", "deploy:7" ]

    # A failed forget: still GO, with a warning.
    @run = BackupRun.request!(@project)
    @token = BackupRun.claim!(@run)
    fake = backup_docker(->(args) { args.include?("forget") } => failure("Fatal: unable to create lock in backend: repository is already locked exclusively\n"))
    back_up(fake)
    assert_equal [ "go", SNAPSHOT ], [ @run.status, @run.snapshot_id ]
    assert_match "old snapshots weren't forgotten: Fatal: unable to create lock", @run.error

    # No forget after a NO-GO.
    @run = BackupRun.request!(@project)
    @token = BackupRun.claim!(@run)
    fake = backup_docker(->(args) { args.include?(StorageLocation::RESTIC_IMAGE) && args.include?("backup") } => failure("Fatal: repository not found\n"))
    back_up(fake)
    assert_equal "no_go", @run.status
    assert_empty restic_forgets(fake)
  end

  # A bug or a surprise mid-backup still finishes the run, and still cleans up.
  test "an unexpected error finishes the run NO-GO" do
    fake = backup_docker(->(args) { args.include?("/rails/lib/backup/sqlite.rb") } => -> { raise NoMethodError, "undefined method 'x' for nil" })
    assert_raises(NoMethodError) { back_up(fake) }
    @run.reload
    assert_equal "no_go", @run.status
    assert_match "Houston failed during the backup: NoMethodError: undefined method 'x' for nil", @run.error
    assert_equal [ "volume", "rm", "-f", "houston-backup.equip" ], fake.calls.last.args
  end

  test "nothing to back up" do
    @project.update!(volumes: [], databases: [])
    fake = FakeDocker.new
    back_up(fake) # and no forget either
    assert_equal [ "skipped", "nothing to back up (no named volumes, no Postgres)" ], [ @run.status, @run.error ]
    assert_empty fake.calls
  end

  test "a hung backup is stopped" do
    fake = backup_docker(->(args) { args.include?(StorageLocation::RESTIC_IMAGE) } => failure("", code: 124))
    back_up(fake)
    assert_equal [ "no_go", "took longer than 3 hours; stopped" ], [ @run.status, @run.error ]
    # Killing the docker CLI leaves its container running: removed at the
    # start (leftovers) and again after the timeout.
    removals = fake.calls.count { |c| c.args == [ "rm", "-f", "houston-backup.equip.sqlite", "houston-backup.equip.restic", "houston-backup.equip.write" ] }
    assert_equal 2, removals
    assert_equal [ "volume", "rm", "-f", "houston-backup.equip" ], fake.calls.last.args
    # Every command gets what's left of the 3 hours.
    assert fake.calls.all? { |c| c.timeout.is_a?(Integer) && c.timeout.between?(1, 3.hours.to_i) }, fake.calls.map(&:timeout).inspect
  end
end

# The heartbeat is a thread with its own database connection, so this test
# commits for real (no wrapping transaction) and cleans up after itself.
class BackupHeartbeatTest < ActiveSupport::TestCase
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers
  self.use_transactional_tests = false

  teardown do
    BackupRun.delete_all
    Deploy.delete_all
    ProjectHost.delete_all
    Project.delete_all
  end

  test "the heartbeat moves while a command runs" do
    project = make_backup_project
    run = BackupRun.request!(project)
    token = BackupRun.claim!(run)
    run.update_columns(heartbeat_at: 1.minute.ago)
    before = run.reload.heartbeat_at
    fake = backup_docker(->(args) { args.include?(StorageLocation::RESTIC_IMAGE) } => -> { sleep 0.5; DockerCommand::Result.new(success: true, output: summary_line) })

    with_heartbeat_every(0.1) { use_fake_docker(fake) { Backup.new(run, token).call } }

    assert_equal "go", run.reload.status
    assert_operator run.heartbeat_at, :>, before + 30.seconds
  end

  def with_heartbeat_every(seconds)
    original = Backup.heartbeat_every
    Backup.heartbeat_every = seconds
    yield
  ensure
    Backup.heartbeat_every = original
  end
end
