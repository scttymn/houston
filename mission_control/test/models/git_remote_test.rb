require "test_helper"
require_relative "../support/fake_git"

class GitRemoteTest < ActiveSupport::TestCase
  test "the recorded host keys for a repo" do
    key = ->(_) { RepoLink.generate_key.last.split[0, 2].join(" ") }
    forgejo, github, custom = key.(1), key.(2), key.(3)
    Tempfile.create("known_hosts") do |f|
      f.write("forgejo #{forgejo}\ngithub.com #{github}\n[git.example.com]:2222 #{custom}\n")
      f.flush
      ENV["HOUSTON_KNOWN_HOSTS"] = f.path
      assert_equal [ "forgejo #{forgejo}" ], GitRemote.known_hosts_for("git@forgejo:houston/spike.git")
      assert_equal [ "github.com #{github}" ], GitRemote.known_hosts_for("ssh://git@github.com/sevenmoons/garage.git")
      assert_equal [ "[git.example.com]:2222 #{custom}" ], GitRemote.known_hosts_for("ssh://git@git.example.com:2222/x/y.git")
      assert_equal [], GitRemote.known_hosts_for("https://github.com/basecamp/kamal.git")
      assert_equal [], GitRemote.known_hosts_for("git@unknown.example:x/y.git")
    end
  ensure
    ENV.delete("HOUSTON_KNOWN_HOSTS")
  end

  test "is a commit still in the repo" do
    extend FakeGitHelper
    project = Project.new(name: "equip", repo_url: "git@forgejo:houston/equip.git", deploy_key_private: "key\n")
    sha = "d4e0b17" + "0" * 33
    found = FakeGit.new { |args| args.include?("rev-parse") ? GitRemote::Result.new(success: true, output: "#{sha}\n") : nil }
    assert use_fake_git(found) { GitRemote.commit(project, sha) }.ok
    fetch = found.calls.map(&:args).find { |a| a.include?("fetch") }
    assert_equal [ "fetch", "--depth", "1", "--no-tags", "--filter=blob:none", "--", "git@forgejo:houston/equip.git", sha ], fetch.drop(fetch.index("fetch"))
    assert found.calls.first.env["GIT_SSH_COMMAND"].include?("-i "), "with the deploy key"

    gone = FakeGit.new { |args| args.include?("fetch") ? GitRemote::Result.new(success: false, output: "fatal: remote error: upload-pack: not our ref\n") : nil }
    result = use_fake_git(gone) { GitRemote.commit(project, sha) }
    assert_not result.ok
    assert_match "not our ref", result.error

    other = FakeGit.new { |args| args.include?("rev-parse") ? GitRemote::Result.new(success: true, output: "#{"e" * 40}\n") : nil }
    assert_not use_fake_git(other) { GitRemote.commit(project, sha) }.ok
  end
end
