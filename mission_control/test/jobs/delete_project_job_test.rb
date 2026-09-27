require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_docker"
require_relative "../support/backup_helpers"
require_relative "../support/tunnel_helpers"

# Deleting a project for real, as docker and Cloudflare commands
# (docs/plans/delete-project.md, Batch 3).
class DeleteProjectJobTest < ActiveJob::TestCase
  include ProjectHelpers
  include FakeDockerHelper
  include BackupHelpers
  include TunnelHelpers

  KAMAL_HOME = "/home/houston/.kamal"
  COMMENT = "managed-by:houston project:equip"
  OTHER_ZONE = "zone-equipping"

  setup do
    ENV["HOUSTON_KAMAL_HOME"] = KAMAL_HOME
    connect_tunnel
    Installation.current.update!(cloudflare_zone_id: ZONE)
    @project = make_backup_project
    @project.update!(domains: %w[equipping.com], repo_url: "git@forgejo:houston/equip.git")
    @project.project_volumes.create!(name: "storage", location: storage_locations(:unas))
    @project.hosts.create!(name: "equip-db-g2")
    @other = make_project("equip-x", services: %w[app db])
    make_deploy(@other, 1, "go")
    stub_registry
  end

  REGISTRY = "http://registry:5000/v2"

  # equip's images in Houston's registry: two commits, latest on the newer.
  def stub_registry
    stub_request(:get, "#{REGISTRY}/equip/tags/list").to_return(status: 200, body: { name: "equip", tags: [ "a" * 40, "latest", "b" * 40 ] }.to_json)
    { "a" * 40 => "sha256:d1", "latest" => "sha256:d2", "b" * 40 => "sha256:d2" }.each do |tag, digest|
      stub_request(:head, "#{REGISTRY}/equip/manifests/#{tag}").to_return(status: 200, headers: { "Docker-Content-Digest" => digest })
    end
    %w[sha256:d1 sha256:d2].each { |digest| stub_request(:delete, "#{REGISTRY}/equip/manifests/#{digest}").to_return(status: 202) }
  end

  teardown { ENV.delete("HOUSTON_KAMAL_HOME") }

  def ask(delete_backups: false)
    ProjectDeletion.request!(@project, confirm: "equip", delete_backups:, by: "admin@example.com")
  end

  # Docker on a server where equip and equip-x both run. overrides as in backup_docker.
  def server(overrides = {})
    backup_docker(overrides.merge({
      ->(a) { a == %w[version --format {{.Server.Version}}] } => DockerCommand::Result.new(success: true, output: "28.4.0\n"),
      ->(a) { a[0..1] == %w[ps -q] && a.include?("label=com.docker.compose.service=registry") } => DockerCommand::Result.new(success: true, output: "5e61571f0001\n"),
      ->(a) { a[0] == "inspect" && a.include?("5e61571f0001") } => DockerCommand::Result.new(success: true, output: [ "PATH=/bin", "REGISTRY_STORAGE_DELETE_ENABLED=true" ].to_json),
      ->(a) { a[0..1] == %w[ps -aq] && a.include?("label=service=equip") } => DockerCommand::Result.new(success: true, output: "c0ffee01\nc0ffee02\n"),
      ->(a) { a[0..1] == %w[ps -aq] && a.include?("name=^/equip-release-[0-9a-f]+$") } => DockerCommand::Result.new(success: true, output: "0e1ea5e0\n"),
      ->(a) { a[0..1] == %w[ps -aq] } => DockerCommand::Result.new(success: true, output: ""),
      ->(a) { a[0..2] == %w[volume ls -q] } => DockerCommand::Result.new(success: true, output: "equip_storage\nequip_pgdata\nequip.g2_storage\nequip-x_storage\nequip-x_pgdata\nkamal-proxy-config\nhouston-backup.equip\n"),
      ->(a) { a[0..1] == %w[images -q] } => DockerCommand::Result.new(success: true, output: "1c1032e9\n1c1032e9\n"),
      ->(a) { a.include?(StorageLocation::RESTIC_IMAGE) && a.include?("snapshots") } =>
        DockerCommand::Result.new(success: true, output: [ snapshot_json(id: "11111111", time: "2026-09-20T03:00:00Z"), snapshot_json(id: "22222222", time: "2026-09-21T03:00:00Z") ].to_json)
    }))
  end

  def records(zone, result) = cf(:get, "/zones/#{zone}/dns_records", query: { "comment.exact" => COMMENT, "per_page" => "5000" }, result:)

  def stub_cloudflare
    cf(:get, "/zones/#{ZONE}/dns_records", query: { "per_page" => "1" }, result: [])
    cf(:get, "/zones", query: { "name" => "equipping.com" }, result: [ { id: OTHER_ZONE, name: "equipping.com", status: "active" } ])
    records(ZONE, [])
    # Cloudflare filters by the exact comment; were it ever loose, the record
    # of equip-x and one without a comment are still never deleted.
    records(OTHER_ZONE, [ { id: "ours", name: "equipping.com", comment: COMMENT },
                          { id: "theirs", name: "x.equipping.com", comment: "managed-by:houston project:equip-x" },
                          { id: "nobodys", name: "y.equipping.com", comment: nil } ])
    cf(:delete, "/zones/#{OTHER_ZONE}/dns_records/ours", result: { id: "ours" })
  end

  def run_it(fake, deletion = ask) = use_fake_docker(fake) { perform_enqueued_jobs(only: DeleteProjectJob) }.then { deletion.reload }

  def removal_calls(fake) = fake.calls.map(&:args).reject { |a| a.include?(StorageLocation::RESTIC_IMAGE) || a[0] == "exec" && a.include?("sh") || a.include?("/rails/lib/backup/sqlite.rb") }

  test "deleting removes everything it owns" do
    stub_cloudflare
    @project.update!(maintenance_since: Time.current, maintenance_by: "admin")
    pushes = record_pushes
    fake = server
    id = @project.id
    deletion = run_it(fake)

    assert_equal [ "go", nil ], [ deletion.status, deletion.error ], deletion.log
    assert_nil Project.find_by(name: "equip")
    assert_equal [ 0, 0, 0, 0, 0 ], [ Secret, Deploy, BackupRun, ProjectVolume, ProjectHost ].map { |m| m.where(project_id: id).count }
    assert_equal [ "equip", "git@forgejo:houston/equip.git", nil ], [ deletion.name, deletion.repo_url, deletion.project_id ]
    assert Project.exists?(name: "equip-x")

    # The final snapshot, kept.
    assert_equal SNAPSHOT, deletion.snapshot_id
    assert_equal storage_locations(:unas), deletion.snapshot_location
    tags = restic_backups(fake).sole.args.each_cons(2).filter_map { |flag, value| value if flag == "--tag" }
    assert_includes tags, "kind:final"
    assert_includes tags, "reason:delete"
    assert_empty restic_forgets(fake), "a final snapshot is never forgotten"

    # Routes: the push leaves equip's hosts out.
    assert_equal [ [] ], pushes.map { |ingress| maintenance_hosts(ingress) }
    assert_requested :delete, "#{API}/zones/#{OTHER_ZONE}/dns_records/ours"
    assert_not_requested :delete, "#{API}/zones/#{OTHER_ZONE}/dns_records/theirs"
    assert_not_requested :delete, "#{API}/zones/#{OTHER_ZONE}/dns_records/nobodys"

    calls = removal_calls(fake)
    tools = BackupHelpers::TOOLS
    expected = [
      %w[version --format {{.Server.Version}}],
      %w[ps -aq --filter label=service=equip],
      %w[rm -f c0ffee01 c0ffee02],
      %w[ps -aq --filter name=^/equip-release-[0-9a-f]+$],
      %w[rm -f 0e1ea5e0],
      %w[rm -f equip-cache],
      %w[rm -f equip-db],
      %w[rm -f equip-db-g2],
      %w[rm -f houston-kamal-equip],
      %w[exec kamal-proxy kamal-proxy remove equip-web],
      %w[volume ls -q],
      %w[ps -aq --filter volume=equip_storage],
      %w[ps -aq --filter volume=equip_pgdata],
      %w[ps -aq --filter volume=equip.g2_storage],
      %w[ps -aq --filter volume=houston-backup.equip],
      %w[volume rm equip_storage],
      %w[volume rm equip_pgdata],
      %w[volume rm equip.g2_storage],
      %w[volume rm houston-backup.equip],
      [ "run", "--rm", "--user", "0", "-v", "houston-storage-unas-nfs:/location", "--entrypoint", "sh", tools, "-c", ProjectRemoval::REMOVE_FOLDERS, "sh", "equip" ],
      [ "images", "-q", "--filter", "reference=127.0.0.1:5000/equip:*" ],
      %w[rmi -f 1c1032e9],
      [ "run", "--rm", "--user", "0", "-v", "#{KAMAL_HOME}:/kamal", "-v", "/var/lib/houston/runners:/runners", "--entrypoint", "sh", tools, "-c", ProjectRemoval::REMOVE_FILES, "sh", "equip" ]
    ]
    removal = calls.drop_while { |a| a != %w[ps -aq --filter label=service=equip] }
    assert_equal expected.drop(1), removal.reject { |a| a[0..1] == [ "volume", "inspect" ] || a[0..1] == [ "volume", "create" ] }
    assert_equal expected.first, calls.first
    assert_no_match(/equip-x/, calls.flatten.join(" "), "nothing of equip-x is named")
    # Its manifests in the registry, each digest once; then the space is freed apart from this job.
    assert_requested :delete, "#{REGISTRY}/equip/manifests/sha256:d1", times: 1
    assert_requested :delete, "#{REGISTRY}/equip/manifests/sha256:d2", times: 1
    assert_includes deletion.log, "deleted 2 images from the registry"
    assert_enqueued_with(job: RegistryCleanupJob, args: [ deletion ])
    assert_includes deletion.log, "removed equip's containers"
  end

  test "a project whose name starts the same is untouched" do
    stub_cloudflare
    fake = server
    run_it(fake)
    named = fake.calls.flat_map(&:args).grep(/equip-x/)
    assert_empty named
    assert_equal %w[equip-x equip-x-db], @other.hosts.pluck(:name).sort
  end

  test "cancelled before anything is removed" do
    cancelled = lambda do |message, fake: server, &arrange|
      ProjectDeletion.delete_all
      WebMock.reset!
      record_pushes
      stub_cloudflare
      arrange&.call
      deletion = run_it(fake)
      assert_equal "no_go", deletion.status, message
      assert_match(/\Acancelled: .*#{Regexp.escape(message)}/, deletion.error)
      assert_nil deletion.removing_at
      assert_equal false, @project.reload.deleting?, "#{message}: the project serves as before"
      # The final snapshot's own helpers (houston-backup.equip*) come and go as in any backup.
      removed = fake.calls.map(&:args).reject { |a| a.join(" ").include?("houston-backup.equip") }
                    .select { |a| a[0].in?(%w[rm rmi]) || a[0..1] == %w[volume rm] || a.include?("kamal-proxy") && a.include?("remove") }
      assert_empty removed, message
      assert_not_requested :delete, %r{dns_records/}
    end

    cancelled.("Docker didn't answer", fake: server(->(a) { a[0] == "version" } => failure("Cannot connect to the Docker daemon")))
    cancelled.("Cloudflare said no: Invalid API Token") { cf(:get, "/zones/#{ZONE}/dns_records", query: { "per_page" => "1" }, status: 401, errors: [ { code: 1000, message: "Invalid API Token" } ]) }
    cancelled.("the final snapshot failed", fake: server(/pg_database/ => failure("connection refused")))
    cancelled.("the registry doesn't allow deletes yet: run the installer once more",
               fake: server(->(a) { a[0] == "inspect" && a.include?("5e61571f0001") } => DockerCommand::Result.new(success: true, output: [ "PATH=/bin" ].to_json)))
    ENV.delete("HOUSTON_KAMAL_HOME")
    cancelled.("run the installer once more")
    ENV["HOUSTON_KAMAL_HOME"] = KAMAL_HOME
    storage_locations(:unas).update!(acknowledged_at: nil)
    cancelled.("no backup storage to keep a final snapshot in")
  end

  test "the final snapshot is skipped when there's nothing to keep" do
    stub_cloudflare
    @project.deploys.delete_all
    fake = server
    deletion = run_it(fake)
    assert_equal "go", deletion.status, deletion.error
    assert_empty restic_backups(fake)
    assert_nil deletion.snapshot_id
  end

  test "stopped partway, then resumed" do
    stub_cloudflare
    stopped = lambda do |step, fake|
      deletion = run_it(fake, ask)
      assert_equal [ "no_go", step ], [ deletion.status, deletion.step ], deletion.error
      assert_match(/\Astopped at #{step}: /, deletion.error)
      assert deletion.removing_at
      assert @project.reload.deleting?, "past the point of no return the project stays deleting"
      deletion
    end

    first = stopped.("containers", server(->(a) { a[0..1] == %w[rm -f] && a.include?("c0ffee01") } => failure("Error response from daemon: cannot remove container: device busy")))
    assert_match "device busy", first.error
    # A container that's already gone is done, not a failure.
    second = stopped.("volumes", server(->(a) { a == %w[rm -f equip-db] } => failure("Error response from daemon: No such container: equip-db"),
                                        ->(a) { a == %w[volume rm equip_pgdata] } => failure("Error response from daemon: remove equip_pgdata: volume is in use")))
    assert_equal first, second

    fake = server(->(a) { a[0..1] == %w[volume rm] } => failure("Error response from daemon: get equip_storage: no such volume"),
                  ->(a) { a.include?("kamal-proxy") && a.include?("remove") } => failure("Error: service not found"))
    done = run_it(fake, ask)
    assert_equal [ "go", nil ], [ done.status, done.error ], done.log
    assert_equal first, done
    assert_empty restic_backups(fake), "a resumed deletion doesn't take another final snapshot"
    assert_nil Project.find_by(name: "equip")
  end

  test "each removal step stops the deletion when it fails" do
    stub_cloudflare
    {
      "dns" => -> { cf(:delete, "/zones/#{OTHER_ZONE}/dns_records/ours", status: 500, errors: [ { code: 1, message: "try again" } ]) },
      "images" => ->(a) { a[0] == "rmi" },
      "registry" => -> { stub_request(:delete, "#{REGISTRY}/equip/manifests/sha256:d1").to_return(status: 500, body: "{}") },
      "files" => ->(a) { a.include?(ProjectRemoval::REMOVE_FILES) },
      "folders" => ->(a) { a.include?(ProjectRemoval::REMOVE_FOLDERS) }
    }.each do |step, breaker|
      ProjectDeletion.delete_all
      fake = if breaker.arity == 0
        breaker.call
        server
      else
        server(breaker => failure("rm: can't remove: Permission denied"))
      end
      deletion = run_it(fake)
      assert_equal [ "no_go", step ], [ deletion.status, deletion.step ], "#{step}: #{deletion.error}"
      assert Project.exists?(name: "equip"), step
      stub_cloudflare
      stub_registry
    end
  end

  test "deleting the backups too" do
    stub_cloudflare
    other = StorageLocation.create!(name: "b2-offsite", kind: "b2", settings: { "bucket" => "b" }, restic_password: "p", verified_at: Time.current, acknowledged_at: Time.current)
    @project.backup_runs.create!(location: other, kind: "auto", reason: "manual", status: "go", heartbeat_at: Time.current)
    fake = server
    deletion = run_it(fake, ask(delete_backups: true))

    assert_equal "go", deletion.status, deletion.error
    assert_empty restic_backups(fake), "no final snapshot"
    forgets = restic_forgets(fake)
    assert_equal 2, forgets.size, "each location the backups used"
    forgets.each do |call|
      assert_includes call.args, "--prune"
      assert_equal [ "11111111".ljust(64, "0"), "22222222".ljust(64, "0") ], call.args.grep(/\A\h{64}\z/)
    end

    ProjectDeletion.delete_all
    @project = make_backup_project
    @project.update!(repo_url: "git@forgejo:houston/equip.git")
    deletion = run_it(server(->(a) { a.include?(StorageLocation::RESTIC_IMAGE) && a.include?("forget") } => failure("repository is already locked")), ask(delete_backups: true))
    assert_equal [ "no_go", "backups" ], [ deletion.status, deletion.step ]
    assert_match "repository is already locked", deletion.error
  end

  test "a job for a deletion that isn't queued does nothing" do
    deletion = ask
    deletion.update!(status: "running", heartbeat_at: Time.current)
    fake = server
    use_fake_docker(fake) { DeleteProjectJob.perform_now(deletion) }
    assert_empty fake.calls
  end
end
