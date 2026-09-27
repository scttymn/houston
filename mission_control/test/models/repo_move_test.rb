require "test_helper"
require_relative "../support/project_helpers"
require_relative "../support/fake_git"

# A project's repo moved or renamed on its git host (a copy keeps the old
# project's URL): the URL changes, once Houston can read the new one with
# the project's deploy key and its compose.yml names the project.
class RepoMoveTest < ActiveSupport::TestCase
  include ProjectHelpers
  include FakeGitHelper

  NEW_URL = "git@github.com:scttymn/equip.git"

  setup do
    @project = make_linked_project("equip")
    @project.update!(seen_refs: { "refs/heads/main" => "a" * 40 })
  end

  def repo(name: "equip", heads: "#{"a" * 40}\trefs/heads/main\n", clone_fails: false)
    FakeGit.new do |args|
      if args.include?("ls-remote") then git_ok(heads)
      elsif args.include?("clone") && clone_fails then git_failure("fatal: Could not read from remote repository.")
      elsif args.include?("rev-parse") then git_ok("#{"a" * 40}\n")
      elsif args.include?("inspect") then git_ok({ "sync" => { "name" => name } }.to_json)
      end
    end
  end

  def move(url = NEW_URL, git: repo) = use_fake_git(git) { RepoMove.new(@project, url).call! }

  test "moving the repo" do
    before = @project.slice(:deploy_key_private, :webhook_secret, :seen_refs, :branch, :compose_path)
    git = repo
    move(NEW_URL, git:)
    @project.reload
    assert_equal NEW_URL, @project.repo_url
    assert_equal before, @project.slice(:deploy_key_private, :webhook_secret, :seen_refs, :branch, :compose_path)
    assert git.calls.any? { |c| c.args.include?(NEW_URL) && c.args.include?("ls-remote") }, "read at the new URL"
  end

  test "moving the repo, refused" do
    refused = lambda do |message, url = NEW_URL, git: repo|
      error = assert_raises(RepoMove::Refused) { move(url, git:) }
      assert_match message, error.message
      assert_equal "git@forgejo:houston/equip.git", @project.reload.repo_url, message
    end
    refused.("must be an ssh://, https:// or user@host:path repo URL", "-oProxyCommand=evil")
    refused.("equip's repo is already git@forgejo:houston/equip.git", "git@forgejo:houston/equip.git")
    refused.("has no branch main", git: repo(heads: "#{"a" * 40}\trefs/heads/trunk\n"))
    refused.("couldn't read git@github.com:scttymn/equip.git", git: repo(clone_fails: true))
    refused.("compose.yml there names shop, not equip", git: repo(name: "shop"))
    make_deploy(@project, 1, "in_flight")
    refused.("deploy #1 is in flight; wait for #1")
  end
end
