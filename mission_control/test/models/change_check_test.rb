require "test_helper"
require_relative "../support/fake_git"
require_relative "../support/project_helpers"

class ChangeCheckTest < ActiveSupport::TestCase
  include FakeGitHelper
  include ProjectHelpers

  A = "a" * 40
  B = "b" * 40
  C = "c" * 40

  def refs(lines) = FakeGit.new { |args, _| args[1] == "ls-remote" ? git_ok(lines.map { |ref, sha| "#{sha}\t#{ref}\n" }.join) : git_ok }

  test "a moved branch queues its commit" do
    project = make_linked_project("garage")
    use_fake_git(refs("refs/heads/main" => A, "refs/heads/dev" => B)) do |git|
      ChangeCheck.new(project).run
      assert_equal [ "git", "ls-remote", "--heads", "--tags", "--", project.repo_url ], git.calls.last.args
      assert_match(/-o IdentitiesOnly=yes/, git.calls.last.env["GIT_SSH_COMMAND"])
    end
    queued = project.deploys.sole
    assert_equal [ "queued", A, "refs/heads/main", 1 ], [ queued.status, queued.sha, queued.ref, queued.number ]
    assert_equal({ "refs/heads/main" => A }, project.reload.seen_refs)
    assert project.last_checked_at

    use_fake_git(refs("refs/heads/main" => A, "refs/heads/dev" => C)) { ChangeCheck.new(project.reload).run }
    assert_equal 1, project.deploys.count, "nothing moved on main"
  end

  test "a new tag queues its commit" do
    project = make_linked_project("garage", deploy_rule: { "on" => "tag", "tags" => "v*" })
    use_fake_git(refs("refs/heads/main" => A, "refs/tags/beta" => B, "refs/tags/v1.0" => C, "refs/tags/v1.0^{}" => B)) do
      ChangeCheck.new(project).run
    end
    queued = project.deploys.sole
    assert_equal [ B, "refs/tags/v1.0" ], [ queued.sha, queued.ref ], "an annotated tag deploys the commit it points at"
  end

  test "one queued deploy, always the newest" do
    project = make_linked_project("garage")
    use_fake_git(refs("refs/heads/main" => A)) { ChangeCheck.new(project).run }
    use_fake_git(refs("refs/heads/main" => B)) { ChangeCheck.new(project.reload).run }
    queued = project.deploys.sole
    assert_equal [ "queued", B, 1 ], [ queued.status, queued.sha, queued.number ]
    assert_match(/switched to #{B.first(7)}/, queued.log)

    queued.update!(status: "in_flight")
    use_fake_git(refs("refs/heads/main" => C)) { ChangeCheck.new(project.reload).run }
    assert_equal [ [ 1, "in_flight", B ], [ 2, "queued", C ] ], project.deploys.order(:number).pluck(:number, :status, :sha)
  end

  test "a failed check queues nothing" do
    project = make_linked_project("garage")
    use_fake_git(FakeGit.new { git_failure("fatal: Could not read from remote repository.\n") }) { ChangeCheck.new(project).run }
    assert_equal 0, project.deploys.count
    assert_match(/Could not read from remote repository/, project.reload.last_check_error)
    assert_equal({}, project.seen_refs)

    use_fake_git(refs("refs/heads/main" => A)) { ChangeCheck.new(project).run }
    assert_nil project.reload.last_check_error
  end

  test "a push waits while a restore is queued or in flight" do
    project = make_linked_project("garage")
    project.update!(seen_refs: { "refs/heads/main" => "a" * 40 })
    project.deploys.create!(number: 1, sha: "c" * 40, ref: "refs/restore/cccccccc", kind: "restore", status: "in_flight", token_digest: "d", heartbeat_at: Time.current)
    queued = use_fake_git(FakeGit.new { |args| args[1] == "ls-remote" ? git_ok("#{"b" * 40}\trefs/heads/main\n") : git_ok }) { ChangeCheck.new(project).run }
    assert_empty queued
    assert_equal({ "refs/heads/main" => "a" * 40 }, project.reload.seen_refs, "the next check after the restore queues the push")
    assert_raises(ChangeCheck::Failed) { use_fake_git(FakeGit.new { git_ok("#{"b" * 40}\trefs/heads/main\n") }) { ChangeCheck.new(project).queue_head! } }
  end

  # A webhook and the poll can check at once (security fixes, M4). A check
  # that loaded the project before the other queued and a runner claimed
  # doesn't queue the same commit again.
  test "a check that read the project before another one queued doesn't queue again" do
    project = make_linked_project("garage")
    first, second = ChangeCheck.new(Project.find(project.id)), ChangeCheck.new(Project.find(project.id))
    use_fake_git(refs("refs/heads/main" => A)) do
      first.run
      project.deploys.sole.update!(status: "in_flight")
      assert_empty second.run
    end
    assert_equal 1, project.deploys.count
  end
end
