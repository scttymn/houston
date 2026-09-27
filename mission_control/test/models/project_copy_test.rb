require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_git"
require_relative "../support/handover_helpers"

# Asking for a copy (docs/plans/copy-project.md, Batch 3): the old project's
# latest deploy held because its compose.yml names a new project; the admin
# asks, and the new project is made from the branch head, with the old one's
# secrets, placement, target and link, and its copy deploy queued.
class ProjectCopyTest < ActiveSupport::TestCase
  include ProjectHelpers
  include FakeGitHelper
  include HandoverHelpers
  include ActiveJob::TestHelper

  HEAD = "c0bb1e5" + "0" * 33

  setup do
    @old = make_linked_project("equip-go", services: %w[app db], variables: [ { "name" => "POSTGRES_PASSWORD", "required" => true }, { "name" => "SENTRY_DSN", "required" => false } ])
    @old.update!(volumes: [ { "name" => "storage", "path" => "/rails/storage" } ], databases: [ { "service" => "db", "image" => "postgres:17" } ],
                 domains: %w[equip.svnmns.com], seen_refs: { "refs/heads/main" => HEAD }, chosen_backup_location: storage_locations(:unas),
                 webhook_verified_at: 1.day.ago)
    @old.secrets.create!(key: "POSTGRES_PASSWORD", value: "pg-secret")
    @old.secrets.create!(key: "SENTRY_DSN", value: "https://sentry.example/1")
    @old.project_volumes.create!(name: "storage", location: storage_locations(:unas), placed_at: 2.days.ago)
    make_deploy(@old, 1, "go")
    make_deploy(@old, 2, "hold", sha: HEAD).update!(proposed_name: "equip")
  end

  # What houston inspect --json says of the branch head.
  def inspection(name: "equip", variables: [ { name: "POSTGRES_PASSWORD", required: true }, { name: "NEW_ONE", required: false } ], extra: {})
    { "sync" => { "name" => name, "app_service" => "app", "services" => %w[app db], "domains" => [], "variables" => variables,
                  "health" => "/up", "port" => 3000, "deploy_rule" => { "on" => "commit", "branch" => "main", "tags" => "v*" },
                  "volumes" => [ { "name" => "storage", "path" => "/rails/storage" } ], "databases" => [ { "service" => "db", "image" => "postgres:17" } ],
                  "backups" => { "schedule" => "daily 04:00", "keep_auto" => 7, "keep_deploy" => 5 } }.deep_stringify_keys.merge(extra),
      "preview" => {} }
  end

  def repo(read = inspection, sha: HEAD)
    FakeGit.new do |args|
      if args.include?("rev-parse") then git_ok("#{sha}\n")
      elsif args.include?("inspect") then git_ok(read.to_json)
      end
    end
  end

  def ask(confirm: "equip-go", git: repo) = use_fake_git(git) { ProjectCopy.request!(@old, confirm:, by: "one@example.com") }

  def refused(message, **options)
    before = [ Project.count, ProjectCopy.count, Deploy.count ]
    error = assert_raises(ProjectCopy::Refused) { ask(**options) }
    assert_match message, error.message
    assert_equal before, [ Project.count, ProjectCopy.count, Deploy.count ], "#{message}: nothing made"
  end

  test "asking for a copy makes the new project and queues its copy" do
    copy = ask
    new = Project.find_by!(name: "equip")

    assert_equal [ @old, new, "equip-go", "equip", HEAD, "one@example.com", "queued" ],
                 [ copy.from_project, copy.project, copy.from, copy.to, copy.sha, copy.by, copy.status ]
    # compose.yml's, from the branch head.
    assert_equal [ %w[app db], "/up", 3000, "daily 04:00", [] ], [ new.services, new.health, new.port, new.backup_schedule, new.domains ]
    assert_equal %w[equip equip-db], new.hosts.pluck(:name).sort
    # The old project's link, key and webhook secret: its git host's webhook keeps working.
    assert_equal @old.slice(:repo_url, :branch, :compose_path, :deploy_key_private, :deploy_key_public, :webhook_secret, :webhook_verified_at, :seen_refs),
                 new.slice(:repo_url, :branch, :compose_path, :deploy_key_private, :deploy_key_public, :webhook_secret, :webhook_verified_at, :seen_refs)
    # Secrets the new compose.yml references, their values copied; placement and target.
    assert_equal({ "POSTGRES_PASSWORD" => "pg-secret" }, new.secrets.to_h { |s| [ s.key, s.value ] })
    assert_equal [ [ "storage", storage_locations(:unas), nil ] ], new.project_volumes.map { |v| [ v.name, v.location, v.placed_at ] }
    assert_equal storage_locations(:unas), new.chosen_backup_location

    deploy = new.deploys.sole
    assert_equal [ 1, "copy", "queued", HEAD, "refs/heads/main", "equip" ], [ deploy.number, deploy.kind, deploy.status, deploy.sha, deploy.ref, deploy.sync_payload["name"] ]
    assert_equal deploy, copy.deploy

    @old.reload
    assert_equal [ "equip-go", 2, %w[equip-go equip-go-db] ], [ @old.name, @old.deploys.count, @old.hosts.pluck(:name).sort ]
    assert_equal [ copy ], @old.copies_from.to_a
  end

  test "asking for a copy, refused" do
    refused("type equip-go to confirm", confirm: "equip")
    refused("equip-go's compose.yml on main now names equip2", git: repo(inspection(name: "equip2")))
    refused("couldn't read git@forgejo:houston/equip-go.git", git: FakeGit.new { |args| git_failure("fatal: Could not read from remote repository.") if args.include?("clone") })
    refused("health: must be a path starting with /", git: repo(inspection.tap { |i| i["sync"]["health"] = "no-slash" }))

    make_project("equip")
    refused("equip is another project")
    Project.find_by!(name: "equip").destroy!

    other = make_project("elsewhere")
    other.hosts.create!(name: "equip-db")
    refused("equip-db")
    other.destroy!

    make_deploy(@old, 3, "queued")
    refused("deploy #3 is queued; wait for #3")
    @old.deploys.find_by!(number: 3).update!(status: "hold", proposed_name: "equip")

    run = @old.backup_runs.create!(location: storage_locations(:unas), kind: "auto", reason: "manual", status: "running", heartbeat_at: Time.current)
    refused("a backup of equip-go is running; wait for it")
    run.update!(status: "go")

    @old.update!(chosen_backup_location: nil)
    storage_locations(:unas).update!(default: false)
    refused("no backup storage for the copy's snapshot of equip-go's data")
    storage_locations(:unas).update!(default: true)

    ask
    refused("equip-go is already being copied to equip")
  end

  test "no proposal, no copy" do
    make_deploy(@old, 3, "go")
    refused("equip-go's latest deploy doesn't propose a copy")
  end

  test "reserved names" do
    @old.deploys.find_by!(number: 2).update!(proposed_name: "hooks")
    refused("hooks can't be a project's name", git: repo(inspection(name: "hooks")))
  end

  test "while the copy is under way, the new project queues nothing of its own" do
    ask
    new = Project.find_by!(name: "equip")
    moved = FakeGit.new { git_ok("#{"f" * 40}\trefs/heads/main\n") }
    use_fake_git(moved) { assert_equal [], ChangeCheck.new(new).run }
    assert_equal [ "copy" ], new.deploys.pluck(:kind)
    assert_equal HEAD, new.deploys.sole.sha, "a push doesn't switch the queued copy"
  end

  test "cancelling before the handover ends the copy's deploy, and the new project goes" do
    copy = ask
    new = copy.project
    copy.cancel!(by: "one@example.com")
    assert_equal [ "no_go", "cancelled by one@example.com" ], [ copy.deploy.reload.status, copy.deploy.error ]
    assert_equal "no_go", copy.reload.status
    assert_enqueued_with(job: CopyCleanupJob, args: [ copy ])
    perform_enqueued_jobs(only: CopyCleanupJob)
    assert new.deleting?
    assert_equal "equip-go", @old.reload.name

    new.destroy! # its deletion, done
    other = ask # the old project's deploy still holds: it can be asked again
    other.update!(status: "running", handed_over_at: Time.current, handed_over: %w[equip.svnmns.com])
    assert_match "equip has taken over equip-go's hosts; undo the copy instead", assert_raises(ProjectCopy::Refused) { other.cancel!(by: "one") }.message
    other.update!(status: "go")
    assert_match "is done", assert_raises(ProjectCopy::Refused) { other.cancel!(by: "one") }.message
  end

  test "a cleanup the new project's restore holds up is tried again" do
    copy = ask
    new = copy.project
    new.backup_runs.create!(location: storage_locations(:unas), kind: "restore", operation: "restore", reason: "restore", deploy_number: 1, status: "running", heartbeat_at: Time.current)
    copy.cancel!(by: "one")
    assert_enqueued_with(job: CopyCleanupJob) { perform_enqueued_jobs(only: CopyCleanupJob) }
    assert_not new.deleting?
  end

  test "undoing a copy: the hosts go back, then the new project is deleted, its backups kept" do
    copy = ask
    new = copy.project
    copy.update!(status: "go", handed_over: %w[equip.svnmns.com], handed_over_at: Time.current)
    new.deploys.sole.update!(status: "go")
    backs = 0
    deletion = with_handover(handover_doing(back: -> { backs += 1 })) { copy.undo!(confirm: "equip", by: "one@example.com") }
    assert_equal 1, backs
    assert_equal [ new, false, "one@example.com" ], [ deletion.project, deletion.delete_backups, deletion.by ]
    assert copy.reload.undone_at

    assert_match "type equip to confirm", assert_raises(ProjectCopy::Refused) { copy.undo!(confirm: "nope", by: "one") }.message
  end

  test "undo is refused once the old project is gone, or when the hosts don't move" do
    copy = ask
    copy.update!(status: "go", handed_over: %w[equip.svnmns.com], handed_over_at: Time.current)
    copy.project.deploys.sole.update!(status: "go")
    failing = handover_doing(back: -> { raise Handover::Failed, "equip-go-web isn't in kamal-proxy any more" })
    error = assert_raises(ProjectCopy::Refused) { with_handover(failing) { copy.undo!(confirm: "equip", by: "one") } }
    assert_match "equip-go-web isn't in kamal-proxy", error.message
    assert_not copy.project.deleting?

    copy.update!(from_project: nil)
    assert_match "equip-go is deleted: there's nothing to go back to", assert_raises(ProjectCopy::Refused) { copy.undo!(confirm: "equip", by: "one") }.message
  end
end
